package tests

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"net"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/cryptix/go/logging/logtest"
	"github.com/go-kit/kit/log"
	goon "github.com/shurcooL/go-goon"
	"github.com/stretchr/testify/require"
	"go.cryptoscope.co/librarian"
	"go.cryptoscope.co/margaret"
	muxtest "go.cryptoscope.co/muxrpc/test"
	"go.cryptoscope.co/netwrap"
	ssb "go.cryptoscope.co/sbot"
	"go.cryptoscope.co/sbot/message"
	"go.cryptoscope.co/sbot/sbot"
)

func writeFile(t *testing.T, data string) string {
	r := require.New(t)
	f, err := ioutil.TempFile("", t.Name())
	r.NoError(err)
	_, err = fmt.Fprintf(f, "%s", data)
	r.NoError(err)
	err = f.Close()
	r.NoError(err)
	return f.Name()
}

func initInterop(t *testing.T, jsbefore, jsafter string, sbotOpts ...sbot.Option) (*sbot.Sbot, *ssb.FeedRef, <-chan struct{}) {
	r := require.New(t)
	ctx := context.Background()

	exited := make(chan struct{})
	dir, err := ioutil.TempDir("", t.Name())
	r.NoError(err, "failed to create testdir for repo")

	// Choose you logger!
	// use the "logtest" line if you want to log through calls to `t.Log`
	// use the "NewLogfmtLogger" line if you want to log to stdout
	// the test logger does not print anything if the command hangs, so you have an alternative
	info, _ := logtest.KitLogger("go", t)
	//info := log.NewLogfmtLogger(log.NewSyncWriter(os.Stdout))

	// timestamps!
	info = log.With(info, "ts", log.TimestampFormat(time.Now, "3:04:05.000"))

	// prepend defaults
	sbotOpts = append([]sbot.Option{
		sbot.WithInfo(info),
		sbot.WithListenAddr("localhost:0"),
		sbot.WithRepoPath(dir),
		sbot.WithContext(ctx),
	}, sbotOpts...)

	sbot, err := sbot.New(sbotOpts...)
	r.NoError(err, "failed to init test go-sbot")
	t.Logf("go-sbot: %s", sbot.KeyPair.Id.Ref())

	go func() {
		err := sbot.Node.Serve(ctx)
		if err != nil {
			fmt.Fprintln(os.Stderr, "warn: go-sbot muxrpc exited", err)
			// t.Fatal(err) BUG?!
			os.Exit(42)
		}
	}()

	pr, pw := io.Pipe()
	cmd := exec.Command("node", "./sbot.js")
	cmd.Stderr = logtest.Logger("js", t)
	cmd.Stdout = pw
	cmd.Env = []string{
		"TEST_NAME=" + t.Name(),
		"TEST_BOB=" + sbot.KeyPair.Id.Ref(),
		"TEST_GOADDR=" + netwrap.GetAddr(sbot.Node.GetListenAddr(), "tcp").String(),
		"TEST_BEFORE=" + writeFile(t, jsbefore),
		"TEST_AFTER=" + writeFile(t, jsafter),
	}

	r.NoError(cmd.Start(), "failed to init test js-sbot")

	// var foo *int
	go func() {
		err := cmd.Wait()
		if err != nil {
			fmt.Fprintln(os.Stderr, "warn: nodejs exited", err)
			// *foo = 3
			// t.Fatal(err)
			os.Exit(23)
		}
		// r.NoError(err, "js-sbot exited")
		close(exited)
	}()

	pubScanner := bufio.NewScanner(pr) // TODO muxrpc comms?
	r.True(pubScanner.Scan(), "multiple lines of output from js - expected #1 to be alices pubkey/id")

	alice, err := ssb.ParseFeedRef(pubScanner.Text())
	r.NoError(err, "failed to get alice key from JS process")
	t.Logf("JS alice: %s", alice.Ref())

	return sbot, alice, exited
}

func TestFeedFromJS(t *testing.T) {
	r := require.New(t)
	s, alice, exited := initInterop(t, `
	function mkMsg(msg) {
		return function(cb) {
			sbot.publish(msg, cb)
		}
	}
	n = 50
	let msgs = []
	for (var i = n; i>0; i--) {
		msgs.push(mkMsg({type:"test", text:"foo", i:i}))
	}
	series(msgs, function(err, results) {
		t.error(err, "series of publish")
		t.equal(n, results.length, "message count")
		run() // triggers connect and after block
	})
`, `
pull(
	sbot.createUserStream({id:alice.id}),
	pull.collect(function(err, vals){
		t.equal(n, vals.length)
		t.end(err)
		setTimeout(exit, 3000) // give go a chance to get this
	})
)
`)
	<-exited // wait for js do be done

	aliceLog, err := s.UserFeeds.Get(librarian.Addr(alice.ID))
	r.NoError(err)
	seq, err := aliceLog.Seq().Value()
	r.NoError(err)
	r.Equal(seq, margaret.BaseSeq(49))

	for i := 0; i < 50; i++ {
		// only one feed in log - directly the rootlog sequences
		seqMsg, err := aliceLog.Get(margaret.BaseSeq(i))
		r.NoError(err)
		r.Equal(seqMsg, margaret.BaseSeq(i))

		msg, err := s.RootLog.Get(seqMsg.(margaret.BaseSeq))
		r.NoError(err)
		storedMsg, ok := msg.(message.StoredMessage)
		r.True(ok, "wrong type of message: %T", msg)
		r.Equal(storedMsg.Sequence, margaret.BaseSeq(i+1))

		type testWrap struct {
			Author  ssb.FeedRef
			Content struct {
				Type, Text string
				I          int
			}
		}
		var m testWrap
		err = json.Unmarshal(storedMsg.Raw, &m)
		r.NoError(err)
		r.Equal(alice.ID, m.Author.ID, "wrong author")
		r.Equal(m.Content.Type, "test")
		r.Equal(m.Content.Text, "foo")
		r.Equal(m.Content.I, 50-i, "wrong I on msg: %d", i)
	}
}

func TestBlobToJS(t *testing.T) {
	r := require.New(t)

	s, _, exited := initInterop(t, `run()`,
		`sbot.blobs.want("&rCJbx8pzYys3zFkmXyYG6JtKZO9/LX51AMME12+WvCY=.sha256",function(err, has) {
			t.true(has, "got blob")
			t.end(err, "no err")
			exit()
		})`)

	ref, err := s.BlobStore.Put(strings.NewReader("bl0000p123123"))
	r.NoError(err)
	r.Equal("&rCJbx8pzYys3zFkmXyYG6JtKZO9/LX51AMME12+WvCY=.sha256", ref.Ref())
	<-exited
}

func TestBlobFromJS(t *testing.T) {
	r := require.New(t)

	testRef, err := ssb.ParseBlobRef("&w6uP8Tcg6K2QR905Rms8iXTlksL6OD1KOWBxTK7wxPI=.sha256") // foobar
	r.NoError(err)

	tsChan := make(chan *muxtest.Transcript, 1)

	s, _, exited := initInterop(t,
		`pull(
			pull.values([Buffer.from("foobar")]),
			sbot.blobs.add(function(err, id) {
				t.error(err, "added")
				t.equal(id, '&w6uP8Tcg6K2QR905Rms8iXTlksL6OD1KOWBxTK7wxPI=.sha256', "blob id")
				run()
			})
		)`,
		`sbot.blobs.has(
			"&w6uP8Tcg6K2QR905Rms8iXTlksL6OD1KOWBxTK7wxPI=.sha256",
			function(err, has) {
				t.true(has, "should have blob")
				t.end(err)
				setTimeout(exit, 3000)
			})`,
		sbot.WithConnWrapper(func(conn net.Conn) (net.Conn, error) {
			var ts muxtest.Transcript

			conn = muxtest.WrapConn(&ts, conn)
			tsChan <- &ts
			return conn, nil
		}))

	err = s.WantManager.Want(testRef)
	r.NoError(err, ".Want() should not error")

	time.Sleep(5 * time.Second)

	br, err := s.BlobStore.Get(testRef)
	r.NoError(err, "should have blob")

	foobar, err := ioutil.ReadAll(br)
	r.NoError(err, "couldnt read blob")
	r.Equal("foobar", string(foobar))

	<-exited
	ts := <-tsChan
	t.Log(goon.Sdump(ts))
}