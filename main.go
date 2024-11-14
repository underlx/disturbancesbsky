package main

import (
	"context"
	"log"
	"time"

	"github.com/gbl08ma/keybox"
	"github.com/palantir/stacktrace"
	"github.com/underlx/disturbancesbsky/underlxclient"
)

func main() {
	ctx := context.Background()

	log.Println("Starting, opening keybox...")
	secrets, err := keybox.Open("secrets.json")
	if err != nil {
		log.Fatalln(stacktrace.Propagate(err, ""))
	}
	log.Println("Keybox opened")

	underlxEndpoint, ok := secrets.Get("underlxEndpoint")
	if !ok {
		log.Fatalln("underlxEndpoint secret not found in keybox")
	}

	bskyBox, ok := secrets.GetBox("bsky")
	if !ok {
		log.Fatalln("bsky box not found in keybox")
	}

	bskyEndpoint, ok := bskyBox.Get("endpoint")
	if !ok {
		log.Fatalln("endpoint secret not found in bsky keybox")
	}

	bskyHandle, ok := bskyBox.Get("handle")
	if !ok {
		log.Fatalln("handle secret not found in bsky keybox")
	}

	bskyAppPassword, ok := bskyBox.Get("password")
	if !ok {
		log.Fatalln("password secret not found in bsky keybox")
	}

	underlxClient, err := underlxclient.NewClientWithResponses(underlxEndpoint)
	if err != nil {
		panic(stacktrace.Propagate(err, ""))
	}

	bskyClient := NewBskyClient(ctx, bskyEndpoint, bskyHandle, bskyAppPassword)

	err = bskyClient.Connect(ctx)
	if err != nil {
		panic(stacktrace.Propagate(err, ""))
	}

	storageFilename, ok := secrets.Get("storageFilename")
	if !ok {
		log.Fatalln("storageFilename secret not found in keybox")
	}

	storage := NewBotStorage(storageFilename)

	messageTimezone, ok := secrets.Get("messageTimezone")
	if !ok {
		log.Fatalln("messageTimezone secret not found in keybox")
	}

	location := time.FixedZone(messageTimezone, 0)

	websiteLinkTemplate, ok := secrets.Get("websiteLinkTemplate")
	if !ok {
		log.Fatalln("websiteLinkTemplate secret not found in keybox")
	}

	bot := NewBot(underlxClient, bskyClient, storage, location, websiteLinkTemplate)
	for {
		err = bot.Run(ctx, 10*time.Second, 2*time.Minute)
		if err != nil {
			log.Println(stacktrace.Propagate(err, ""))
		}
	}
}
