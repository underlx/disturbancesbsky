package main

import (
	"bytes"
	"context"
	"io"
	"sync"
	"time"

	"net/http"

	"github.com/bluesky-social/indigo/api/atproto"
	"github.com/bluesky-social/indigo/api/bsky"
	"github.com/bluesky-social/indigo/lex/util"
	"github.com/bluesky-social/indigo/xrpc"
	"github.com/palantir/stacktrace"
)

// largely inspired by github.com/danrusei/gobot-bsky

type BskyClient struct {
	mu            sync.Mutex
	client        *xrpc.Client
	handle        string
	apiKey        string
	lastConnected time.Time
}

func NewBskyClient(ctx context.Context, server, handle, apiKey string) *BskyClient {
	return &BskyClient{
		client: &xrpc.Client{
			Client: &http.Client{},
			Host:   server,
		},
		handle: handle,
		apiKey: apiKey,
	}
}

func (b *BskyClient) Connect(ctx context.Context) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	return stacktrace.Propagate(b.connect(ctx), "")
}

func (b *BskyClient) connect(ctx context.Context) error {
	serverCreateSessionInput := &atproto.ServerCreateSession_Input{
		Identifier: b.handle,
		Password:   b.apiKey,
	}

	session, err := atproto.ServerCreateSession(ctx, b.client, serverCreateSessionInput)
	if err != nil {
		return stacktrace.Propagate(err, "")
	}

	b.client.Auth = &xrpc.AuthInfo{
		AccessJwt:  session.AccessJwt,
		RefreshJwt: session.RefreshJwt,
		Handle:     session.Handle,
		Did:        session.Did,
	}

	b.lastConnected = time.Now()

	return nil
}

func (b *BskyClient) connectIfNeeded(ctx context.Context) error {
	if time.Since(b.lastConnected) < 1*time.Minute {
		return nil
	}

	return stacktrace.Propagate(b.connect(ctx), "")
}

func (b *BskyClient) Post(ctx context.Context, post bsky.FeedPost) (string, string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.connectIfNeeded(ctx)

	postInput := &atproto.RepoCreateRecord_Input{
		Collection: "app.bsky.feed.post",
		Repo:       b.client.Auth.Did,
		Record: &util.LexiconTypeDecoder{
			Val: &post,
		},
	}

	response, err := atproto.RepoCreateRecord(ctx, b.client, postInput)
	if err != nil {
		return "", "", stacktrace.Propagate(err, "")
	}

	return response.Cid, response.Uri, nil
}

func (b *BskyClient) UploadImage(ctx context.Context, client *http.Client, imageURL string) (*util.LexBlob, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.connectIfNeeded(ctx)

	getImage, err := getImageAsBuffer(client, imageURL)
	if err != nil {
		return nil, stacktrace.Propagate(err, "")
	}

	resp, err := atproto.RepoUploadBlob(ctx, b.client, bytes.NewReader(getImage))
	if err != nil {
		return nil, stacktrace.Propagate(err, "")
	}

	blob := util.LexBlob{
		Ref:      resp.Blob.Ref,
		MimeType: resp.Blob.MimeType,
		Size:     resp.Blob.Size,
	}

	return &blob, nil
}

func getImageAsBuffer(client *http.Client, imageURL string) ([]byte, error) {
	// Fetch image
	response, err := client.Get(imageURL)
	if err != nil {
		return nil, stacktrace.Propagate(err, "")
	}
	defer response.Body.Close()

	// Check response status
	if response.StatusCode != http.StatusOK {
		return nil, stacktrace.NewError("failed to fetch image: %s", response.Status)
	}

	// Read response body
	imageData, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, stacktrace.Propagate(err, "")
	}

	return imageData, nil
}
