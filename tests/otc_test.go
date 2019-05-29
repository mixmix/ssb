package tests

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"go.cryptoscope.co/librarian"
	"go.cryptoscope.co/margaret"
	"go.cryptoscope.co/ssb"
	"go.cryptoscope.co/ssb/message"
)

func TestContentFeedFromGo(t *testing.T) {
	r := require.New(t)

	ts := newRandomSession(t)
	// ts := newSession(t, nil, nil)

	ts.startGoBot()
	s := ts.gobot

	before := `fromKey = testBob
	sbot.on('rpc:connect', (rpc) => {
		rpc.on('closed', () => {
			t.comment('now should have feed:' + fromKey)
			pull(
				sbot.contentStream.getContentStream({id: fromKey}),
				pull.collect((err, msgs) => {
					t.error(err)
					console.warn('BHC: '+msgs.length)
					console.warn(JSON.stringify(msgs))
					t.end()
				})
			)
		})
		setTimeout(() => {
			t.comment('now should have feed:' + fromKey)
			pull(
				sbot.contentStream.getContentStream({id: fromKey}),
				pull.collect((err, msgs) => {
					t.error(err)
					console.warn('Messages: '+msgs.length)
					console.warn(JSON.stringify(msgs))
					// t.end()
				})
			)
		},1000)
	})

	sbot.publish({type: 'contact', contact: fromKey, following: true}, function(err, msg) {
		t.error(err, 'follow:' + fromKey)

		sbot.friends.get({src: alice.id, dest: fromKey}, function(err, val) {
			t.error(err, 'friends.get of new contact')
			t.equals(val[alice.id], true, 'is following')

			t.comment('shouldnt have bobs feed:' + fromKey)
			pull(
				sbot.createUserStream({id:fromKey}),
				pull.collect(function(err, vals){
					t.error(err)
					t.equal(0, vals.length)
					sbot.publish({type: 'about', about: fromKey, name: 'test bob'}, function(err, msg) {
						t.error(err, 'about:' + msg.key)
						setTimeout(run, 3000) // give go bot a moment to publish
					})
				})
			)

}) // friends.get

}) // publish`

	alice := ts.startJSBot(before, "")

	n := 5
	brs := make([]*ssb.BlobRef, n)
	for i := 0; i < n; i++ {
		var err error
		blobMsg := fmt.Sprintf(`{ "type": "hello", "world": true, "i": %d}`, i)
		brs[i], err = s.BlobStore.Put(strings.NewReader(blobMsg))
		r.NoError(err)
		// t.Log(brs[i].Ref())
	}

	var tmsgs = []interface{}{
		map[string]interface{}{
			"type": "blob-message",
			"blob": brs[0].Ref(),
		},
		map[string]interface{}{
			"type":      "contact",
			"contact":   alice.Ref(),
			"following": true,
		},
		map[string]interface{}{
			"type":  "about",
			"about": alice.Ref(),
			"image": brs[3].Ref(),
		},
		map[string]interface{}{
			"type": "text",
			"text": `# hello world!`,
			"blob": brs[1].Ref(),
		},
		map[string]interface{}{
			"type": "blob",
			"blob": brs[2].Ref(),
		},
		// brs[3],
		// brs[4],
	}
	for i, msg := range tmsgs {
		newSeq, err := s.PublishLog.Append(msg)
		r.NoError(err, "failed to publish test message %d", i)
		r.NotNil(newSeq)
	}
	t.Fail()
	<-ts.doneJS

	aliceLog, err := s.UserFeeds.Get(librarian.Addr(alice.ID))
	r.NoError(err)

	seqMsg, err := aliceLog.Get(margaret.BaseSeq(1))
	r.NoError(err)
	msg, err := s.RootLog.Get(seqMsg.(margaret.BaseSeq))
	r.NoError(err)
	storedMsg, ok := msg.(message.StoredMessage)
	r.True(ok, "wrong type of message: %T", msg)
	r.Equal(storedMsg.Sequence, margaret.BaseSeq(2))

	ts.wait()
}
