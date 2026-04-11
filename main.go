package main

import (
	"embed"

	"github.com/mercereau/hydrolix-metrics-go/cmd"
)

//go:embed queries/*
var queriesFS embed.FS

func main() {
	cmd.SetQueriesFS(queriesFS)
	cmd.Execute()
}
