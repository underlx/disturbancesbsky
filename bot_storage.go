package main

import (
	"encoding/gob"
	"errors"
	"os"

	"github.com/palantir/stacktrace"
)

type BotStorageModel struct {
	KnownDisturbances []KnownDisturbance
}

type KnownDisturbance struct {
	ID            string
	KnownStatuses []KnownStatus
}

type KnownStatus struct {
	ID          string
	BSkyPostCID string
	BSkyPostURI string
}

type BotStorage struct {
	filename string
}

func NewBotStorage(filename string) *BotStorage {
	return &BotStorage{
		filename: filename,
	}
}

func (b *BotStorage) Get() (BotStorageModel, error) {
	f, err := os.Open(b.filename)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return BotStorageModel{
				KnownDisturbances: []KnownDisturbance{},
			}, nil
		}
		return BotStorageModel{}, stacktrace.Propagate(err, "")
	}
	defer f.Close()

	dec := gob.NewDecoder(f)
	var model BotStorageModel
	err = dec.Decode(&model)
	if err != nil {
		return BotStorageModel{}, stacktrace.Propagate(err, "")
	}

	return model, nil
}

func (b *BotStorage) Put(model BotStorageModel) error {
	f, err := os.Create(b.filename)
	if err != nil {
		return stacktrace.Propagate(err, "")
	}
	defer f.Close()

	enc := gob.NewEncoder(f)
	err = enc.Encode(model)
	if err != nil {
		return stacktrace.Propagate(err, "")
	}

	return nil
}
