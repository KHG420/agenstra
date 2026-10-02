package main

import (
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
)

func modelCommand(args []string, request func(string, string, any) (any, error)) (any, error) {
	if len(args) == 0 {
		return nil, errors.New("model list | configure FILE --revision N | check PROFILE [--purpose decision|memory_extraction] [--config FILE]")
	}
	switch args[0] {
	case "list":
		if len(args) != 1 {
			return nil, errors.New("model list takes no arguments")
		}
		return request("GET", "/admin/api/models", nil)
	case "configure", "check":
		if len(args) < 2 {
			return nil, errors.New("configuration file or profile required")
		}
		fs := flag.NewFlagSet("model "+args[0], flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		revision := fs.Int("revision", -1, "expected catalog revision")
		purpose := fs.String("purpose", "decision", "decision or memory_extraction")
		configPath := fs.String("config", "", "optional unsaved configuration")
		if err := fs.Parse(args[2:]); err != nil {
			return nil, err
		}
		if fs.NArg() != 0 {
			return nil, errors.New("unexpected model arguments")
		}
		readConfig := func(path string) (json.RawMessage, error) {
			raw, err := os.ReadFile(path)
			if err != nil {
				return nil, err
			}
			if !json.Valid(raw) {
				return nil, errors.New("model configuration must be JSON")
			}
			return json.RawMessage(raw), nil
		}
		if args[0] == "configure" {
			if *revision < 0 || *configPath != "" || *purpose != "decision" {
				return nil, errors.New("model configure FILE requires --revision N")
			}
			config, err := readConfig(args[1])
			if err != nil {
				return nil, err
			}
			return request("PUT", "/admin/api/models", map[string]any{"expected_revision": *revision, "config": config})
		}
		if *revision != -1 || *purpose != "decision" && *purpose != "memory_extraction" {
			return nil, errors.New("invalid model check purpose or revision")
		}
		body := map[string]any{"profile": args[1], "purpose": *purpose}
		if *configPath != "" {
			config, err := readConfig(*configPath)
			if err != nil {
				return nil, err
			}
			body["config"] = config
		}
		return request("POST", "/admin/api/models/check", body)
	default:
		return nil, errors.New("unknown model command")
	}
}
