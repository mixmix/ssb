package multilogs

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"sync"
	"time"

	"github.com/pkg/errors"
	"go.cryptoscope.co/librarian"
	"go.cryptoscope.co/luigi"
	"go.cryptoscope.co/margaret"
	"go.cryptoscope.co/margaret/multilog"

	"go.cryptoscope.co/ssb"
	"go.cryptoscope.co/ssb/message"
)

type publishLog struct {
	sync.Mutex
	margaret.Log
	rootLog margaret.Log
	key     ssb.KeyPair
	hmac    *[32]byte

	offchain     bool
	setTimestamp bool
}

/* Get retreives the message object by traversing the authors sublog to the root log
func (pl publishLog) Get(s margaret.Seq) (interface{}, error) {

	idxv, err := pl.authorLog.Get(s)
	if err != nil {
		return nil, errors.Wrap(err, "publish get: failed to retreive sequence for the root log")
	}

	msgv, err := pl.rootLog.Get(idxv.(margaret.Seq))
	if err != nil {
		return nil, errors.Wrap(err, "publish get: failed to retreive message from rootlog")
	}
	return msgv, nil
}

TODO: do the same for Query()? but how?

=> just overwrite publish on the authorLog for now
*/
func (pl *publishLog) Append(val interface{}) (margaret.Seq, error) {
	pl.Lock()
	defer pl.Unlock()

	// prepare persisted message
	var stored message.StoredMessage
	stored.Timestamp = time.Now() // "rx"
	stored.Author = pl.key.Id

	// set metadata
	var newMsg message.LegacyMessage
	newMsg.Author = pl.key.Id.Ref()
	newMsg.Hash = "sha256"

	if pl.setTimestamp {
		newMsg.Timestamp = time.Now().UnixNano() / 1000000
	}

	if pl.offchain {
		buf := new(bytes.Buffer)
		h := sha256.New()

		enc := json.NewEncoder(io.MultiWriter(buf, h))
		if err := enc.Encode(val); err != nil {
			return nil, errors.Wrap(err, "publishLog: failed to encode offchain msg")
		}

		or := &ssb.OffchainRef{
			Hash: h.Sum(nil),
			Algo: ssb.RefAlgoSHA256,
		}

		stored.Offchain = buf.Bytes()
		newMsg.Content = or.Ref()
	} else {
		newMsg.Content = val
	}

	// current state of the local sig-chain
	currSeq, err := pl.Seq().Value()
	if err != nil {
		return nil, errors.Wrap(err, "publishLog: failed to establish current seq")
	}
	seq := currSeq.(margaret.Seq)
	currRootSeq, err := pl.Get(seq)
	if err != nil && !luigi.IsEOS(err) {
		return nil, errors.Wrap(err, "publishLog: failed to retreive current msg")
	}
	if luigi.IsEOS(err) { // new feed
		newMsg.Previous = nil
		newMsg.Sequence = 1
	} else {
		currV, err := pl.rootLog.Get(currRootSeq.(margaret.Seq))
		if err != nil {
			return nil, errors.Wrap(err, "publishLog: failed to establish current seq")
		}

		currMsg, ok := currV.(message.StoredMessage)
		if !ok {
			return nil, errors.Errorf("publishLog: invalid value at sequence %v: %T", currSeq, currV)
		}

		newMsg.Previous = currMsg.Key
		newMsg.Sequence = margaret.BaseSeq(currMsg.Sequence + 1)
	}

	mr, signedMessage, err := newMsg.Sign(pl.key.Pair.Secret[:], pl.hmac)
	if err != nil {
		return nil, err
	}

	stored.Previous = newMsg.Previous
	stored.Sequence = newMsg.Sequence
	stored.Key = mr
	stored.Raw = signedMessage

	_, err = pl.rootLog.Append(stored)
	if err != nil {
		return nil, errors.Wrap(err, "failed to append new msg")
	}

	log.Println("new message key:", mr.Ref())
	return newMsg.Sequence - 1, nil
}

// OpenPublishLog needs the base datastore (root or receive log - offset2)
// and the userfeeds with all the sublog and uses the passed keypair to find the corresponding user feed
// the returned sink is then used to create new messages.
// warning: it is assumed that the
// these messages are constructed in the legacy SSB way: The poured object is JSON v8-like pretty printed and then NaCL signed,
// then it's pretty printed again (now with the signature inside the message) to construct it's SHA256 hash,
// which is used to reference it (by replys and it's previous)
func OpenPublishLog(rootLog margaret.Log, sublogs multilog.MultiLog, kp ssb.KeyPair, opts ...PublishOption) (margaret.Log, error) {

	if sublogs == nil {
		return nil, errors.Errorf("no sublog for publish")
	}
	authorLog, err := sublogs.Get(librarian.Addr(kp.Id.ID))
	if err != nil {
		return nil, errors.Wrap(err, "publish: failed to open sublog for author")
	}

	pl := &publishLog{
		Log:     authorLog,
		rootLog: rootLog,
		key:     kp,
	}

	for i, o := range opts {
		if err := o(pl); err != nil {
			return nil, errors.Wrapf(err, "publish: option %d failed", i)
		}
	}

	return pl, nil
}

type PublishOption func(*publishLog) error

func SetHMACKey(hmackey []byte) PublishOption {
	return func(pl *publishLog) error {
		var hmacSec [32]byte
		if n := copy(hmacSec[:], hmackey); n != 32 {
			return fmt.Errorf("hmac key of wrong length:%d", n)
		}
		pl.hmac = &hmacSec
		return nil
	}
}

func EnableOffchain(yes bool) PublishOption {
	return func(pl *publishLog) error {
		pl.offchain = yes
		return nil
	}
}

func UseNowTimestamps(yes bool) PublishOption {
	return func(pl *publishLog) error {
		pl.setTimestamp = yes
		return nil
	}
}
