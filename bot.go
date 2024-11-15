package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/bluesky-social/indigo/api/atproto"
	"github.com/bluesky-social/indigo/api/bsky"
	"github.com/palantir/stacktrace"
	"github.com/samber/lo"
	"github.com/underlx/disturbancesbsky/underlxclient"
	"golang.org/x/text/cases"
	"golang.org/x/text/language"
)

type Bot struct {
	underlxClient       *underlxclient.ClientWithResponses
	bskyClient          *BskyClient
	embedsClient        *http.Client
	storage             *BotStorage
	timestampLocation   *time.Location
	websiteLinkTemplate string
	postLangs           []string

	model BotStorageModel
}

func NewBot(underlxClient *underlxclient.ClientWithResponses, bskyClient *BskyClient, storage *BotStorage, timestampLocation *time.Location, websiteLinkTemplate string, postLangs []string) *Bot {
	return &Bot{
		underlxClient:       underlxClient,
		bskyClient:          bskyClient,
		embedsClient:        &http.Client{},
		storage:             storage,
		timestampLocation:   timestampLocation,
		websiteLinkTemplate: websiteLinkTemplate,
		postLangs:           postLangs,
	}
}

func (b *Bot) Run(ctx context.Context, checkInterval time.Duration, iterationTimeout time.Duration) error {
	var err error
	b.model, err = b.storage.Get()
	if err != nil {
		return stacktrace.Propagate(err, "")
	}
	t := time.NewTicker(checkInterval)
	for {
		ctx2, c := context.WithTimeout(ctx, iterationTimeout)
		err := b.tick(ctx2)
		c()
		if err != nil {
			return stacktrace.Propagate(err, "")
		}

		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
	}
}

func (b *Bot) tick(ctx context.Context) error {
	disturbances, err := b.underlxClient.ListDisturbancesWithResponse(ctx, &underlxclient.ListDisturbancesParams{
		Filter:              lo.ToPtr(underlxclient.ListDisturbancesParamsFilterOngoing),
		Omitduplicatestatus: lo.ToPtr(true),
	})
	if err != nil {
		return stacktrace.Propagate(err, "")
	}
	if disturbances.StatusCode() != http.StatusOK {
		return stacktrace.NewError("failed to fetch ongoing disturbances: %s", disturbances.Status())
	}

	seenDisturbancesSet := make(map[string]struct{})

	for _, disturbance := range lo.FromPtr(disturbances.JSON200) {
		if !lo.FromPtr(disturbance.Official) {
			continue
		}

		seenDisturbancesSet[disturbance.Id.String()] = struct{}{}
	}

	for knownDisturbanceID := range b.model.KnownDisturbances {
		if _, ok := seenDisturbancesSet[knownDisturbanceID]; !ok {
			// disturbance disappeared, fetch it and post any statuses we haven't posted yet
			disturbanceResp, err := b.underlxClient.GetDisturbanceWithResponse(ctx, knownDisturbanceID, &underlxclient.GetDisturbanceParams{
				Omitduplicatestatus: lo.ToPtr(true),
			})
			if err != nil {
				return stacktrace.Propagate(err, "")
			}
			if disturbanceResp.StatusCode() != http.StatusOK {
				return stacktrace.NewError("failed to fetch disturbance %s: %s", knownDisturbanceID, disturbanceResp.Status())
			}

			disturbance := lo.FromPtr(disturbanceResp.JSON200)

			updatedStatuses := lo.FromPtr(disturbance.Statuses)
			knownStatuses := b.model.KnownDisturbances[knownDisturbanceID].KnownStatuses
			newStatuses := updatedStatuses[min(len(updatedStatuses), len(knownStatuses)):]

			for _, newStatus := range newStatuses {
				if !lo.FromPtr(newStatus.OfficialSource) {
					continue
				}

				knownStatus, err := b.sendPostForStatus(ctx, knownStatuses, disturbance, newStatus)
				if err != nil {
					return stacktrace.Propagate(err, "")
				}
				// it's important that we do this despite deleting the disturbance from the storage right after,
				// because otherwise sendPostForStatus won't be able to chain the replies properly
				knownStatuses = append(knownStatuses, knownStatus)
				d := b.model.KnownDisturbances[knownDisturbanceID]
				d.KnownStatuses = knownStatuses
				b.model.KnownDisturbances[knownDisturbanceID] = d

				// save early, save often, so we don't repeat messages
				err = b.storage.Put(b.model)
				if err != nil {
					return stacktrace.Propagate(err, "")
				}
			}

			// remove from storage model
			delete(b.model.KnownDisturbances, knownDisturbanceID)

			// save early, save often, so we don't repeat messages
			err = b.storage.Put(b.model)
			if err != nil {
				return stacktrace.Propagate(err, "")
			}
		}
	}

	for _, disturbance := range lo.FromPtr(disturbances.JSON200) {
		if !lo.FromPtr(disturbance.Official) {
			continue
		}
		if knownDisturbance, ok := b.model.KnownDisturbances[disturbance.Id.String()]; ok {
			// check if there are any new statuses
			updatedStatuses := lo.FromPtr(disturbance.Statuses)
			newStatuses := updatedStatuses[min(len(updatedStatuses), len(knownDisturbance.KnownStatuses)):]

			for _, newStatus := range newStatuses {
				if !lo.FromPtr(newStatus.OfficialSource) {
					continue
				}
				knownStatus, err := b.sendPostForStatus(ctx, knownDisturbance.KnownStatuses, disturbance, newStatus)
				if err != nil {
					return stacktrace.Propagate(err, "")
				}
				knownDisturbance.KnownStatuses = append(knownDisturbance.KnownStatuses, knownStatus)
				b.model.KnownDisturbances[disturbance.Id.String()] = knownDisturbance

				// save early, save often, so we don't repeat messages
				err = b.storage.Put(b.model)
				if err != nil {
					return stacktrace.Propagate(err, "")
				}
			}
		} else {
			// new disturbance
			// send posts for all statuses
			knownDisturbance.ID = disturbance.Id.String()

			// add to storage model
			b.model.KnownDisturbances[knownDisturbance.ID] = knownDisturbance

			// save early, save often, so we don't miss sending a disturbance if we fail to send any posts
			err = b.storage.Put(b.model)
			if err != nil {
				return stacktrace.Propagate(err, "")
			}

			for _, status := range lo.FromPtr(disturbance.Statuses) {
				if !lo.FromPtr(status.OfficialSource) {
					continue
				}

				knownStatus, err := b.sendPostForStatus(ctx, knownDisturbance.KnownStatuses, disturbance, status)
				if err != nil {
					return stacktrace.Propagate(err, "")
				}
				knownDisturbance.KnownStatuses = append(knownDisturbance.KnownStatuses, knownStatus)
				b.model.KnownDisturbances[knownDisturbance.ID] = knownDisturbance

				// save early, save often, so we don't repeat messages
				err = b.storage.Put(b.model)
				if err != nil {
					return stacktrace.Propagate(err, "")
				}
			}
		}
	}

	return nil
}

func (b *Bot) sendPostForStatus(ctx context.Context, sentStatuses []KnownStatus, disturbance underlxclient.Disturbance, status underlxclient.LineStatus) (KnownStatus, error) {
	post := bsky.FeedPost{
		LexiconTypeID: "app.bsky.feed.post",
		CreatedAt:     lo.FromPtrOr(status.Time, time.Now()).Format(time.RFC3339),
		Langs:         b.postLangs,
	}

	if len(sentStatuses) > 0 {
		post.Reply = &bsky.FeedPost_ReplyRef{
			Root: &atproto.RepoStrongRef{
				Cid: sentStatuses[0].BSkyPostCID,
				Uri: sentStatuses[0].BSkyPostURI,
			},
			Parent: &atproto.RepoStrongRef{
				Cid: sentStatuses[len(sentStatuses)-1].BSkyPostCID,
				Uri: sentStatuses[len(sentStatuses)-1].BSkyPostURI,
			},
		}
	}

	textBuilder := strings.Builder{}

	if disturbance.Line != nil {
		textBuilder.WriteString("Linha ")
		textBuilder.WriteString(cases.Title(language.Portuguese, cases.NoLower).String(strings.TrimPrefix(*disturbance.Line, "pt-ml-")))
		textBuilder.WriteString(" ")
	}

	textBuilder.WriteString("(")
	textBuilder.WriteString(lo.FromPtr(status.Time).In(b.timestampLocation).Format("15:04"))
	textBuilder.WriteString("): ")

	switch {
	case !lo.FromPtr(status.Downtime):
		textBuilder.WriteString("🟢 ")
	case strings.Contains(lo.FromPtr(status.MsgType), "HALT"):
		textBuilder.WriteString("🔴 ")
	default:
		textBuilder.WriteString("🟠 ")
	}

	textBuilder.WriteString(lo.FromPtr(status.Status))

	post.Text = textBuilder.String()

	if len(post.Text) > 300 {
		post.Text = post.Text[:295]
		post.Text += "(…)"
		post.Facets = []*bsky.RichtextFacet{
			{
				Index: &bsky.RichtextFacet_ByteSlice{
					ByteStart: 295,
					ByteEnd:   300,
				},
				Features: []*bsky.RichtextFacet_Features_Elem{
					{
						RichtextFacet_Link: &bsky.RichtextFacet_Link{
							LexiconTypeID: "app.bsky.richtext.facet#link",
							Uri:           fmt.Sprintf(b.websiteLinkTemplate, disturbance.Id.String()),
						},
					},
				},
			},
		}
	}

	var err error
	post.Embed, err = b.produceEmbedForDisturbance(disturbance.Id.String())
	if err != nil {
		return KnownStatus{}, stacktrace.Propagate(err, "")
	}

	cid, uri, err := b.bskyClient.Post(ctx, post)
	if err != nil {
		return KnownStatus{}, stacktrace.Propagate(err, "")
	}

	log.Println("Created post with URI", uri)

	return KnownStatus{
		ID:          status.Id.String(),
		BSkyPostCID: cid,
		BSkyPostURI: uri,
	}, nil
}

func (b *Bot) produceEmbedForDisturbance(id string) (*bsky.FeedPost_Embed, error) {
	uri := fmt.Sprintf(b.websiteLinkTemplate, id)

	embed := &bsky.FeedPost_Embed{
		EmbedExternal: &bsky.EmbedExternal{
			LexiconTypeID: "app.bsky.embed.external",
			External: &bsky.EmbedExternal_External{
				Uri: uri,
			},
		},
	}

	response, err := b.embedsClient.Get(uri)
	if err != nil {
		return nil, stacktrace.Propagate(err, "")
	}

	doc, err := goquery.NewDocumentFromReader(response.Body)
	if err != nil {
		return nil, stacktrace.Propagate(err, "")
	}

	embed.EmbedExternal.External.Title = doc.Find("title").First().Text()
	embed.EmbedExternal.External.Description = doc.Find("meta[name=description]").First().AttrOr("content", "")
	imageURL := doc.Find("meta[property=og\\:image]").First().AttrOr("content", "")

	if len(imageURL) > 0 {
		image, err := b.bskyClient.UploadImage(context.Background(), b.embedsClient, imageURL)
		if err != nil {
			return nil, stacktrace.Propagate(err, "")
		}
		embed.EmbedExternal.External.Thumb = image
	}

	return embed, nil
}
