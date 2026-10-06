package main

import (
    "testing"
    "github.com/Paca-AI/plugin-sdk-go/plugintest"
)

func TestHealthRequiresMigration(t *testing.T) {
    c := plugintest.NewContext(t)
    p := &integrationPlugin{}
    if err := p.Init(c.PluginContext()); err != nil { t.Fatal(err) }
    r := c.Call("GET", "/health", plugintest.Request{})
    if r.StatusCode != 503 { t.Fatalf("unmigrated schema must be unavailable: %s", r.BodyString()) }
}
