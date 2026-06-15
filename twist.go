// Package twist provides functions to work with Twist API.
//
// It relies on Twist API v3 documented at https://developer.twist.com/v3/
package twist

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Client is a Twist API client.
type Client struct {
	token string
}

// New returns Client that calls Twist API using provided token for
// authentication.
//
// See https://developer.twist.com/v3/#authentication for details.
func New(token string) *Client { return &Client{token: token} }

type User struct {
	Id        uint64 `json:"id"`
	Name      string `json:"name"`
	ShortName string `json:"short_name"`
}

func (c *Client) Users(ctx context.Context, workspaceID uint64) ([]User, error) {
	if workspaceID == 0 {
		return nil, errors.New("invalid workspace id")
	}
	vals := make(url.Values)
	vals.Add("id", strconv.FormatUint(workspaceID, 10))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://comms.todoist.com/api/v4/workspace_users/get"+"?"+vals.Encode(), nil)
	if err != nil {
		return nil, err
	}
	setAuthHeader(req, c.token)
	body, err := doRequestWithRetries(req)
	if err != nil {
		return nil, err
	}
	defer body.Close()
	var out []User
	if err := json.NewDecoder(body).Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}

// Thread returns a single thread. Use [ThreadsPaginator] to get all threads of a channel.
func (c *Client) Thread(ctx context.Context, threadID uint64) (*Thread, error) {
	if threadID == 0 {
		return nil, errors.New("invalid thread id")
	}
	vals := make(url.Values)
	vals.Add("id", strconv.FormatUint(threadID, 10))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://comms.todoist.com/api/v3/threads/getone"+"?"+vals.Encode(), nil)
	if err != nil {
		return nil, err
	}
	setAuthHeader(req, c.token)
	body, err := doRequestWithRetries(req)
	if err != nil {
		return nil, err
	}
	defer body.Close()
	var out Thread
	if err := json.NewDecoder(body).Decode(&out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Workspaces returns all the workspaces user has access to.
func (c *Client) Workspaces(ctx context.Context) ([]Workspace, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://comms.todoist.com/api/v3/workspaces/get", nil)
	if err != nil {
		return nil, err
	}
	setAuthHeader(req, c.token)
	body, err := doRequestWithRetries(req)
	if err != nil {
		return nil, err
	}
	defer body.Close()
	var out []Workspace
	if err := json.NewDecoder(body).Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}

// Channels returns all the channels in a given workspace.
func (c *Client) Channels(ctx context.Context, workspaceID uint64) ([]Channel, error) {
	if workspaceID == 0 {
		return nil, errors.New("invalid workspace id")
	}
	vals := make(url.Values)
	vals.Add("workspace_id", strconv.FormatUint(workspaceID, 10))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://comms.todoist.com/api/v3/channels/get"+"?"+vals.Encode(), nil)
	if err != nil {
		return nil, err
	}
	setAuthHeader(req, c.token)
	body, err := doRequestWithRetries(req)
	if err != nil {
		return nil, err
	}
	defer body.Close()
	var out []Channel
	if err := json.NewDecoder(body).Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}

// Workspace is a Twist workspace. A workspace is a shared place between
// different users. Workspace contains channels.
//
// See https://developer.twist.com/v3/#workspaces for details.
type Workspace struct {
	Id   uint64 `json:"id"`
	Name string `json:"name"`
}

// Channel is a Twist channel. Channels organize threads around broad topics
// like team, project, location, or area of interest. Channel contains threads.
//
// See https://developer.twist.com/v3/#channels for details.
type Channel struct {
	Id       uint64 `json:"id"`
	Name     string `json:"name"`
	Archived bool   `json:"archived"`
}

// Thread is a Twist thread. Threads keep team's conversations organized by
// specific topics. Thread contains comments.
//
// See https://developer.twist.com/v3/#threads for details.
type Thread struct {
	Id        uint64 `json:"id"`
	TsPosted  uint64 `json:"posted_ts"`
	TsUpdated uint64 `json:"last_updated_ts"`
	Title     string `json:"title"`
	Text      string `json:"content"`
	Creator   uint64 `json:"creator"`
	Archived  bool   `json:"is_archived"`
}

// UpdatedAt is a convenience method to convert TsUpdated field to time.
func (t *Thread) UpdatedAt() time.Time { return time.Unix(int64(t.TsUpdated), 0) }

// PostedAt is a convenience method to convert TsPosted field to time.
func (t *Thread) PostedAt() time.Time { return time.Unix(int64(t.TsPosted), 0) }

// Comment is a message posted to a thread.
//
// See https://developer.twist.com/v3/#comments for details.
type Comment struct {
	Id         uint64 `json:"id"`
	Text       string `json:"content"`
	Creator    uint64 `json:"creator"`
	OrderIndex int    `json:"obj_index"`
	TsPosted   uint64 `json:"posted_ts"`
}

// PostedAt is a convenience method to convert TsPosted field to time.
func (c *Comment) PostedAt() time.Time { return time.Unix(int64(c.TsPosted), 0) }

// ThreadsPaginator returns ThreadsPaginator that fetches all threads of a
// channel.
func (c *Client) ThreadsPaginator(channelID uint64) *ThreadsPaginator {
	return &ThreadsPaginator{c: c, channelID: channelID}
}

// NewThreadsPaginator returns ThreadsPaginator that fetches only threads
// updated since given time.
//
// Only use it to update channel thread list that you already have on a
// best-effort basis. Twist API logic is racy and may miss some threads that
// were updated between per-page API calls. If you need to fetch all threads of
// a channel, use ThreadsPaginator method instead.
func (c *Client) NewThreadsPaginator(channelID uint64, since time.Time) *ThreadsPaginator {
	var ts uint64
	if t := since.Unix(); t > 0 {
		ts = uint64(t)
	}
	return &ThreadsPaginator{c: c, channelID: channelID, nextSinceTs: ts}
}

// ThreadsPaginator fetches threads of a channel.
//
// Typical usage:
//
//	p := client.ThreadsPaginator(1234) // get threads for channel with id=1234
//	for p.Next() {
//		threads, err := p.Page(ctx)
//		if err != nil {
//			return err
//		}
//		doSomethingWithThreads(threads)
//	}
type ThreadsPaginator struct {
	c         *Client
	channelID uint64
	afterID   uint64
	done      bool

	// only used when fetching updated threads
	nextSinceTs uint64
}

// Next reports whether there's another page to load. It only returns false
// once all threads are fetched with the Page method.
func (cp *ThreadsPaginator) Next() bool { return !cp.done }

// Page returns next portion of channel threads.
func (cp *ThreadsPaginator) Page(ctx context.Context) ([]Thread, error) {
	if cp.done {
		return nil, errors.New("all pages already read")
	}
	var threads []Thread
	var err error
	if cp.nextSinceTs != 0 {
		threads, err = cp.c.getNewChannelThreadsPage(ctx, cp.channelID, cp.nextSinceTs)
	} else {
		threads, err = cp.c.getChannelThreadsPage(ctx, cp.channelID, cp.afterID)
	}
	if err != nil {
		return nil, err
	}
	if cp.nextSinceTs != 0 && len(threads) != 0 {
		var maxTs uint64
		for _, t := range threads {
			if t.TsUpdated > maxTs {
				maxTs = t.TsUpdated
			}
			if maxTs != 0 {
				// +1 to avoid duplicates on page boundaries, API uses closed
				// interval
				cp.nextSinceTs = maxTs + 1
			}
		}
	}
	cp.done = len(threads) < maxThreadsPerPage
	if l := len(threads); l != 0 {
		cp.afterID = threads[l-1].Id
	}
	return threads, nil
}

// getNewChannelThreadsPage returns chunk of threads using loose window based
// on newer_than_ts API argument. Results aren't suitable to get a complete
// list of channel threads. Results aren't ordered, may contain duplicates, and
// may miss some threads that were updated concurrently with this API call.
func (c *Client) getNewChannelThreadsPage(ctx context.Context, channelID, sinceTimestamp uint64) ([]Thread, error) {
	if channelID == 0 {
		return nil, errors.New("invalid channel ID")
	}
	vals := make(url.Values)
	vals.Add("channel_id", strconv.FormatUint(channelID, 10))
	vals.Add("limit", strconv.Itoa(maxThreadsPerPage))
	vals.Add("newer_than_ts", strconv.FormatUint(sinceTimestamp, 10))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://comms.todoist.com/api/v3/threads/get", strings.NewReader(vals.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	setAuthHeader(req, c.token)
	body, err := doRequestWithRetries(req)
	if err != nil {
		return nil, err
	}
	defer body.Close()

	var out []Thread
	if err := json.NewDecoder(body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decoding response: %w", err)
	}
	return out, nil
}

// getChannelThreadsPage returns chunk of threads using precise window based on
// after_id API argument, suitable to reliably get all channel threads.
func (c *Client) getChannelThreadsPage(ctx context.Context, channelID, afterID uint64) ([]Thread, error) {
	if channelID == 0 {
		return nil, errors.New("invalid channel ID")
	}
	vals := make(url.Values)
	vals.Add("channel_id", strconv.FormatUint(channelID, 10))
	vals.Add("limit", strconv.Itoa(maxThreadsPerPage))
	vals.Add("order_by", "asc")
	if afterID == 0 {
		// Twist has (had?) an issue — it ignored after_id=0, resulting in
		// response sorted by update timestamp instead of id
		vals.Add("after_id", "-1")
	} else {
		vals.Add("after_id", strconv.FormatUint(afterID, 10))
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://comms.todoist.com/api/v3/threads/get", strings.NewReader(vals.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	setAuthHeader(req, c.token)
	body, err := doRequestWithRetries(req)
	if err != nil {
		return nil, err
	}
	defer body.Close()

	var out []Thread
	if err := json.NewDecoder(body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decoding response: %w", err)
	}
	if !sort.SliceIsSorted(out, func(i, j int) bool { return out[i].Id < out[j].Id }) {
		// for _, v := range out {
		// 	log.Println("id:", v.Id, "last_updated:", v.TsUpdated)
		// }
		return nil, errors.New("API returned threads that are not properly sorted by ids")
	}
	return out, nil
}

// CommentsPaginator returns CommentsPaginator that fetches all comments of a thread.
func (c *Client) CommentsPaginator(threadID uint64) *CommentsPaginator {
	return &CommentsPaginator{c: c, threadID: threadID}
}

// NewCommentsPaginator returns CommentsPaginator that fetches only thread
// comments that were posted since given time.
//
// Only use it to update thread comments you already have on a best-effort
// basis. Twist API logic is racy and may miss some comments that were updated
// between per-page API calls. If you need to fetch all comments of a thread,
// use CommentsPaginator method instead.
func (c *Client) NewCommentsPaginator(threadID uint64, since time.Time) *CommentsPaginator {
	var ts uint64
	if t := since.Unix(); t > 0 {
		ts = uint64(t)
	}
	return &CommentsPaginator{c: c, threadID: threadID, nextSinceTs: ts}
}

// CommentsPaginator fetches comments of a thread.
//
// Typical usage:
//
//	p := client.CommentsPaginator(3456) // get comments for thread with id=3456
//	for p.Next() {
//		comments, err := p.Page(ctx)
//		if err != nil {
//			return err
//		}
//		doSomethingWithComments(comments)
//	}
type CommentsPaginator struct {
	c         *Client
	threadID  uint64
	nextIndex int
	done      bool

	// only used when fetching updated comments
	nextSinceTs uint64
}

// Next reports whether there's another page to load. It only returns false
// once all channels are fetched with the Page method.
func (tp *CommentsPaginator) Next() bool { return !tp.done }

// Page returns next portion of thread comments.
func (tp *CommentsPaginator) Page(ctx context.Context) ([]Comment, error) {
	if tp.done {
		return nil, errors.New("all pages already read")
	}
	var comments []Comment
	var err error
	if tp.nextSinceTs != 0 {
		comments, err = tp.c.getNewThreadCommentsPage(ctx, tp.threadID, tp.nextSinceTs)
	} else {
		comments, err = tp.c.getThreadCommentsPage(ctx, tp.threadID, tp.nextIndex)
	}
	if err != nil {
		return nil, err
	}
	if tp.nextSinceTs != 0 && len(comments) != 0 {
		var maxTs uint64
		for _, c := range comments {
			if c.TsPosted > maxTs {
				maxTs = c.TsPosted
			}
		}
		if maxTs != 0 {
			// +1 to avoid duplicates on page boundaries: API uses closed
			// interval and includes comments with posted_ts equal to
			// newer_than_ts argument
			tp.nextSinceTs = maxTs + 1
		}
	}
	tp.done = len(comments) < maxCommentsPerPage
	if l := len(comments); l != 0 {
		tp.nextIndex = comments[l-1].OrderIndex + 1
	}
	return comments, nil
}

// getNewThreadCommentsPage returns chunk of comments using loose window based
// on newer_than_ts API argument. Results aren't suitable to get a complete
// list of thread comments. Results are not ordered, may contain duplicates,
// and may miss some comments that were updated concurrently this API call.
func (c *Client) getNewThreadCommentsPage(ctx context.Context, threadID, sinceTimestamp uint64) ([]Comment, error) {
	if threadID == 0 {
		return nil, errors.New("invalid thread ID")
	}
	vals := make(url.Values)
	vals.Add("thread_id", strconv.FormatUint(threadID, 10))
	vals.Add("limit", strconv.Itoa(maxCommentsPerPage))
	vals.Add("newer_than_ts", strconv.FormatUint(sinceTimestamp, 10))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://comms.todoist.com/api/v3/comments/get"+"?"+vals.Encode(), nil)
	if err != nil {
		return nil, err
	}
	setAuthHeader(req, c.token)
	body, err := doRequestWithRetries(req)
	if err != nil {
		return nil, err
	}
	defer body.Close()

	var out []Comment
	if err := json.NewDecoder(body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decoding response: %w", err)
	}
	return out, nil
}

// getThreadCommentsPage returns chunk of comments using precise window based
// on {from,to}_obj_index API arguments, suitable to reliably get all thread
// comments. Comments returned are ordered by OrderIndex increasing without
// gaps.
func (c *Client) getThreadCommentsPage(ctx context.Context, threadID uint64, fromIndex int) ([]Comment, error) {
	if fromIndex < 0 {
		panic("fromIndex must be non-negative")
	}
	if threadID == 0 {
		return nil, errors.New("invalid thread ID")
	}
	vals := make(url.Values)
	vals.Add("thread_id", strconv.FormatUint(threadID, 10))
	vals.Add("limit", strconv.Itoa(maxCommentsPerPage))
	vals.Add("from_obj_index", strconv.Itoa(fromIndex))
	// API returns results including both {from,to}_obj_index, it calculates
	// result like [from_obj_index, to_obj_index][:limit]
	vals.Add("to_obj_index", strconv.Itoa(fromIndex+maxCommentsPerPage-1))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://comms.todoist.com/api/v3/comments/get"+"?"+vals.Encode(), nil)
	if err != nil {
		return nil, err
	}
	setAuthHeader(req, c.token)
	body, err := doRequestWithRetries(req)
	if err != nil {
		return nil, err
	}
	defer body.Close()

	var out []Comment
	if err := json.NewDecoder(body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decoding response: %w", err)
	}
	if !sort.SliceIsSorted(out, func(i, j int) bool { return out[i].OrderIndex < out[j].OrderIndex }) {
		return nil, errors.New("API returned comments that are not properly sorted by obj_index")
	}
	var offset int
	for i, c := range out { // sanity check
		if i == 0 {
			offset = c.OrderIndex
			continue
		}
		if want := offset + i; c.OrderIndex != want {
			return nil, fmt.Errorf("ordering issue in comments: order index is %d, expected %d",
				c.OrderIndex, want)
		}
	}
	return out, nil
}

// doRequestWithRetries calls http.DefaultClient.Do for a given request. It
// checks that response is 200 OK, and has an "application/json" Content-Type.
// If response code is 429 Too Many Requests, or one of 5xx, function
// automatically retries request up to a limited number of attempts. It returns
// response body on success.
func doRequestWithRetries(req *http.Request) (io.ReadCloser, error) {
	attempt := func(req *http.Request) (body io.ReadCloser, tryAgain bool, err error) {
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return nil, false, err
		}
		var defuseBodyClose bool
		defer func() {
			if defuseBodyClose {
				return
			}
			resp.Body.Close()
		}()
		switch {
		case resp.StatusCode == http.StatusOK:
		case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= http.StatusInternalServerError:
			return nil, true, fmt.Errorf("unexpected status: %q", resp.Status)
		default:
			return nil, false, fmt.Errorf("unexpected status: %q", resp.Status)
		}
		if ct := resp.Header.Get(headerContentType); ct != jsonContentType {
			return nil, false, fmt.Errorf("unexpected Content-Type: %q", ct)
		}
		defuseBodyClose = true
		return resp.Body, false, nil
	}

	var ticker *time.Ticker
	const maxRetries = 10
	var lastError error

	for n := 0; n < maxRetries; n++ {
		if n != 0 && req.Body != nil {
			if seeker, ok := req.Body.(io.Seeker); ok {
				if _, err := seeker.Seek(0, io.SeekStart); err != nil {
					return nil, fmt.Errorf("rewinding request body to 0: %w", err)
				}
			} else {
				return nil, fmt.Errorf("cannot rewind non-nil request body for retry, last error was %w", lastError)
			}
		}
		if n != 0 {
			if ticker == nil {
				ticker = time.NewTicker(500 * time.Millisecond)
				defer ticker.Stop()
			}
			select {
			case <-ticker.C:
			case <-req.Context().Done():
				return nil, req.Context().Err()
			}
		}
		body, tryAgain, err := attempt(req)
		if err != nil {
			lastError = err
			if tryAgain {
				continue
			}
			return nil, err
		}
		return body, nil
	}
	return nil, fmt.Errorf("giving up after %d retries, last error was %w", maxRetries, lastError)
}

func setAuthHeader(r *http.Request, token string) {
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("User-Agent", "github.com/artyom/twist")
}

const jsonContentType = "application/json"
const headerContentType = "Content-Type"

const maxThreadsPerPage = 100
const maxCommentsPerPage = 500
