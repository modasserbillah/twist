package main

import (
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/artyom/twist"
)

func main() {
	log.SetFlags(0)
	cache := flag.Bool("c", false, "cache result for 5 minutes"+
		"\n(you can also enable this with DUMP_TWIST_THREAD_CACHE=1 env)")
	flag.Parse()
	if v, _ := strconv.ParseBool(os.Getenv("DUMP_TWIST_THREAD_CACHE")); v && !*cache {
		*cache = v
	}
	if err := run(context.Background(), *cache, flag.Arg(0)); err != nil {
		log.Fatal(err)
	}
}

func run(ctx context.Context, cache bool, threadURL string) error {
	pruneCache()
	if threadURL == "" {
		return errors.New("want Comms thread url as the first argument")
	}
	token := os.Getenv("COMMS_TOKEN")
	if token == "" {
		return errors.New("please set COMMS_TOKEN env")
	}
	if strings.Contains(threadURL, "/msg/") {
		// TODO: consolidate logic?
		return dumpChat(ctx, cache, token, threadURL)
	}
	ids, err := tidFromURL(threadURL)
	if err != nil {
		return err
	}
	if cache {
		if b := readCache(threadURL); len(b) != 0 {
			_, err = os.Stdout.Write(b)
			return err
		}
	}
	client := twist.New(token)
	users, err := client.Users(ctx, ids.workspace)
	if err != nil {
		return fmt.Errorf("getting workspace users: %w", err)
	}
	uidToName := make(map[uint64]string)
	for _, u := range users {
		uidToName[u.Id] = cmp.Or(u.ShortName, u.Name)
	}
	thread, err := client.Thread(ctx, ids.thread)
	if err != nil {
		return fmt.Errorf("reading thread: %w", err)
	}
	var buf bytes.Buffer
	buf.WriteString("<post>\n")
	fmt.Fprintf(&buf, "<author>%s</author>", cmp.Or(uidToName[thread.Creator], "UNKNOWN USER"))
	fmt.Fprintf(&buf, "<date>%s</date>\n", thread.PostedAt().Format("Monday, 02 Jan 2006"))
	fmt.Fprintf(&buf, "# %s\n\n", thread.Title)
	fmt.Fprintln(&buf, clearMentions(thread.Text))
	buf.WriteString("</post>\n")
	p := client.CommentsPaginator(ids.thread)
	for p.Next() {
		comments, err := p.Page(ctx)
		if err != nil {
			return fmt.Errorf("reading thread comments: %w", err)
		}
		for _, c := range comments {
			buf.WriteString("<comment>\n")
			fmt.Fprintf(&buf, "<author>%s</author>", cmp.Or(uidToName[c.Creator], "UNKNOWN USER"))
			fmt.Fprintf(&buf, "<date>%s</date>\n", c.PostedAt().Format("Monday, 02 Jan 2006"))
			fmt.Fprintln(&buf, clearMentions(c.Text))
			buf.WriteString("</comment>\n")
		}
	}
	if cache {
		writeCache(threadURL, buf.Bytes())
	}
	_, err = os.Stdout.Write(buf.Bytes())
	return err
}

var commsThreadURL = regexp.MustCompile(`^https://comms\.todoist\.com/a/(\d+)/ch/(\d+)/t/(\d+)/?$`)

func tidFromURL(url string) (*tid, error) {
	m := commsThreadURL.FindStringSubmatch(url)
	if m == nil {
		return nil, fmt.Errorf("%q does not match %v", url, commsThreadURL)
	}
	var out tid
	var err error
	if out.workspace, err = strconv.ParseUint(m[1], 10, 64); err != nil {
		return nil, err
	}
	if out.channel, err = strconv.ParseUint(m[2], 10, 64); err != nil {
		return nil, err
	}
	if out.thread, err = strconv.ParseUint(m[3], 10, 64); err != nil {
		return nil, err
	}
	return &out, nil
}

type tid struct {
	workspace, channel, thread uint64
}

var mentionRe = regexp.MustCompile(`\[(?<name>[^\]]+)\]\(twist-mention://\d+\)`)

func clearMentions(text string) string { return mentionRe.ReplaceAllString(text, "${name}") }

func writeCache(url string, data []byte) error {
	if cacheDir == "" {
		return errors.New("cache dir is unknown")
	}
	if url == "" || len(data) == 0 {
		return errors.New("both url and data must be non-empty")
	}
	if err := os.MkdirAll(cacheDir, 0700); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(cacheDir, urlToKey(url)), data, 0600)
}

func readCache(url string) []byte {
	if url == "" || cacheDir == "" {
		return nil
	}
	b, err := os.ReadFile(filepath.Join(cacheDir, urlToKey(url)))
	if err == nil {
		return b
	}
	return nil
}

func urlToKey(url string) string { return fmt.Sprintf("%x.txt", sha256.Sum256([]byte(url))) }

func pruneCache() {
	if cacheDir == "" {
		return
	}
	root, err := os.OpenRoot(cacheDir)
	if err != nil {
		return
	}
	defer root.Close()
	threshold := time.Now().Add(-5 * time.Minute)
	fs.WalkDir(root.FS(), ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.Type().IsRegular() || !strings.HasSuffix(path, ".txt") {
			return nil
		}
		if fi, err := d.Info(); err == nil && fi.ModTime().Before(threshold) {
			_ = root.Remove(path)
		}
		return nil
	})
}

var cacheDir = filepath.Join(os.TempDir(), "dump-twist-thread")

func init() {
	flag.Usage = func() {
		w := flag.CommandLine.Output()
		fmt.Fprintf(w, "Usage: %s URL\n", os.Args[0])
		fmt.Fprintln(w, "URL is a Comms thread url you can get with “Copy link to thread” action")
		flag.PrintDefaults()
	}
}

var commsChatURL = regexp.MustCompile(`^\Qhttps://comms.todoist.com/a/\E(?:\d+)/msg/(\d+)/$`)

func dumpChat(ctx context.Context, cache bool, token, url string) error {
	m := commsChatURL.FindStringSubmatch(url)
	if m == nil {
		return fmt.Errorf("%q does not match %v", url, commsChatURL)
	}

	if cache {
		if b := readCache(url); len(b) != 0 {
			_, err := os.Stdout.Write(b)
			return err
		}
	}

	const limit = 500
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://comms.todoist.com/api/v1/conversation_messages/get?conversation_id="+m[1]+"&limit="+strconv.Itoa(limit), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status: %s", resp.Status)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
		return fmt.Errorf("unexpected content-type: %q", ct)
	}

	dec := json.NewDecoder(resp.Body)
	var out []struct {
		Text      string `json:"content"`
		Author    string `json:"creator_name"`
		Timestamp int64  `json:"posted_ts"`
	}
	if err := dec.Decode(&out); err != nil {
		return err
	}
	var buf bytes.Buffer
	if len(out) == limit {
		buf.WriteString("(earlier messages not shown)\n\n")
	}
	for _, msg := range out {
		fmt.Fprintf(&buf, "<msg><author>%s</author>", msg.Author)
		fmt.Fprintf(&buf, "<date>%s</date>\n", time.Unix(msg.Timestamp, 0).Format("Monday, 02 Jan 2006 15:04"))
		fmt.Fprintln(&buf, clearMentions(msg.Text))
		buf.WriteString("</msg>\n")
	}
	if cache {
		writeCache(url, buf.Bytes())
	}
	_, err = os.Stdout.Write(buf.Bytes())
	return err
}
