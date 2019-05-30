package tests

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.cryptoscope.co/librarian"
	"go.cryptoscope.co/margaret"
	"go.cryptoscope.co/ssb"
	"go.cryptoscope.co/ssb/message"
)

func TestContentFeedFromJS(t *testing.T) {
	a := assert.New(t)
	r := require.New(t)
	const n = 23

	ts := newRandomSession(t)

	ts.startGoBot()
	bob := ts.gobot

	alice := ts.startJSBot(`
	function mkMsg(msg) {
		return function(cb) {
			sbot.contentStream.publish(msg, cb)
		}
	}
	n = 23
	let msgs = []
	for (var i = n; i>0; i--) {
		msgs.push(mkMsg({type:"offchain", text:"foo", test:i}))
	}

	// be done when the other party is done
	sbot.on('rpc:connect', rpc => rpc.on('closed', exit))

	parallel(msgs, function(err, results) {
		t.error(err, "parallel of publish")
		t.equal(n, results.length, "message count")
		run() // triggers connect and after block
	})
`, ``)

	newSeq, err := bob.PublishLog.Append(map[string]interface{}{
		"type":      "contact",
		"contact":   alice.Ref(),
		"following": true,
	})
	r.NoError(err, "failed to publish contact message")
	r.NotNil(newSeq)

	<-ts.doneJS

	aliceLog, err := bob.UserFeeds.Get(librarian.Addr(alice.ID))
	r.NoError(err)
	seq, err := aliceLog.Seq().Value()
	r.NoError(err)
	r.Equal(margaret.BaseSeq(n-1), seq)

	for i := 0; i < n; i++ {
		// only one feed in log - directly the rootlog sequences
		seqMsg, err := aliceLog.Get(margaret.BaseSeq(i))
		r.NoError(err)
		r.Equal(seqMsg, margaret.BaseSeq(i+1))

		msg, err := bob.RootLog.Get(seqMsg.(margaret.BaseSeq))
		r.NoError(err)
		storedMsg, ok := msg.(message.StoredMessage)
		r.True(ok, "wrong type of message: %T", msg)
		r.Equal(storedMsg.Sequence, margaret.BaseSeq(i+1))

		type testWrap struct {
			Author  ssb.FeedRef
			Content struct {
				Type, Text string
				Test       int
			}
		}
		var m testWrap
		err = json.Unmarshal(storedMsg.Raw, &m)
		r.NoError(err)
		a.Equal(alice.ID, m.Author.ID, "wrong author")
		a.Equal(m.Content.Type, "offchain")
		a.Equal(m.Content.Text, "foo")
		a.Equal(m.Content.Test, n-i, "wrong I on msg: %d", i)
		t.Log(string(storedMsg.Raw))
	}
}

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

	var tmsgs = []interface{}{
		map[string]interface{}{
			"type":  "ex-message",
			"hello": "world",
		},
		map[string]interface{}{
			"type":      "contact",
			"contact":   alice.Ref(),
			"following": true,
		},
		map[string]interface{}{
			"type":  "message",
			"text":  "whoops",
			"fault": true,
		},
	}
	for i, msg := range tmsgs {
		newSeq, err := s.PublishLog.Append(msg)
		r.NoError(err, "failed to publish test message %d", i)
		r.NotNil(newSeq)
	}

	<-ts.doneJS

	aliceLog, err := s.UserFeeds.Get(librarian.Addr(alice.ID))
	r.NoError(err)

	seqMsg, err := aliceLog.Get(margaret.BaseSeq(2))
	r.NoError(err)
	msg, err := s.RootLog.Get(seqMsg.(margaret.BaseSeq))
	r.NoError(err)
	storedMsg, ok := msg.(message.StoredMessage)
	r.True(ok, "wrong type of message: %T", msg)
	r.Equal(storedMsg.Sequence, margaret.BaseSeq(3))

	ts.wait()
}
