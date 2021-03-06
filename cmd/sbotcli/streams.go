package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/pkg/errors"
	"go.cryptoscope.co/luigi"
	"go.cryptoscope.co/muxrpc"
	cli "gopkg.in/urfave/cli.v2"
)

type mapMsg map[string]interface{}

func typeStreamCmd(ctx *cli.Context) error {
	src, err := client.Source(longctx, mapMsg{}, muxrpc.Method{"messagesByType"}, ctx.Args().First())
	if err != nil {
		return errors.Wrap(err, "source stream call failed")
	}
	err = luigi.Pump(longctx, jsonDrain(os.Stdout), src)
	return errors.Wrap(err, "byType failed")
}

func historyStreamCmd(ctx *cli.Context) error {
	var args = getStreamArgs(ctx)
	src, err := client.Source(longctx, mapMsg{}, muxrpc.Method{"createHistoryStream"}, args)
	if err != nil {
		return errors.Wrap(err, "source stream call failed")
	}
	err = luigi.Pump(longctx, jsonDrain(os.Stdout), src)
	return errors.Wrap(err, "feed hist failed")
}

func logStreamCmd(ctx *cli.Context) error {
	var args = getStreamArgs(ctx)
	src, err := client.Source(longctx, mapMsg{}, muxrpc.Method{"createLogStream"}, args)
	if err != nil {
		return errors.Wrap(err, "source stream call failed")
	}
	err = luigi.Pump(longctx, jsonDrain(os.Stdout), src)
	return errors.Wrap(err, "log failed")
}

func privateReadCmd(ctx *cli.Context) error {
	var args = getStreamArgs(ctx)
	src, err := client.Source(longctx, mapMsg{}, muxrpc.Method{"private", "read"}, args)
	if err != nil {
		return errors.Wrap(err, "source stream call failed")
	}
	err = luigi.Pump(longctx, jsonDrain(os.Stdout), src)
	return errors.Wrap(err, "private/read failed")
}

func jsonDrain(w io.Writer) luigi.Sink {
	i := 0
	return luigi.FuncSink(func(ctx context.Context, val interface{}, err error) error {
		if luigi.IsEOS(err) {
			return nil
		} else if err != nil {
			return errors.Wrapf(err, "jsonDrain: failed to drain message %d", i)
		}
		b, err := json.MarshalIndent(val, "", "  ")
		if err != nil {
			return errors.Wrapf(err, "jsonDrain: failed to encode msg %d", i)
		}
		_, err = fmt.Fprintln(w, string(b))
		if err != nil {
			return errors.Wrapf(err, "jsonDrain: failed to write msg %d", i)
		}
		i++
		return nil
	})
}

/*

func query(ctx *cli.Context) error {
	reply := make(chan map[string]interface{})
	go func() {
		for r := range reply {
			goon.Dump(r)
		}
	}()
	if err := client.Source("query.read", reply, ctx.Args().Get(0)); err != nil {
		return errors.Wrap(err, "source stream call failed")
	}
	return client.Close()
}







*/
