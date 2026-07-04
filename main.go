package main

import (
	"log"
	"os"

	"github.com/ricoberger/prlsp/internal/github"
	"github.com/ricoberger/prlsp/internal/jsonrpc"
	"github.com/ricoberger/prlsp/internal/server"
)

func main() {
	log.SetOutput(os.Stderr)
	log.SetFlags(log.Ltime | log.Lmicroseconds)

	conn := jsonrpc.New(os.Stdin, os.Stdout)
	server.New(conn, &github.Client{}).Run()
}
