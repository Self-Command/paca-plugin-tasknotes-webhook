package main

import (
    "encoding/json"
    "fmt"
    "os"
)

func main() {
    if len(os.Args) > 1 && os.Args[1] == "--version" {
        _ = json.NewEncoder(os.Stdout).Encode(map[string]string{"plugin":"com.selfcommand.tasknotes-webhook","version":"0.1.0","phase":"host-baseline"})
        return
    }
    fmt.Fprintln(os.Stderr, "Companion worker processing is not yet configured in the phase-0 baseline.")
    os.Exit(78)
}
