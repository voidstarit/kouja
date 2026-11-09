package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"

	"github.com/voidstarit/kouja/internal/restore"
)

func main() {
	archive := flag.String("archive", "", "path to a legacy kouja backup .tar.gz or .tar.gz.enc")
	directory := flag.String("dir", "", "local directory containing plain backup files and restore.json")
	s3URI := flag.String("s3", "", "S3 backup prefix, for example s3://bucket/kouja/2026Nov091648/")
	keyFile := flag.String("key-file", "", "backup encryption key file (required for encrypted backups)")
	configPath := flag.String("config", "/etc/kouja/config.yaml", "destination path for the restored configuration")
	apply := flag.Bool("apply", false, "write restored files; without this flag the command only prints the plan")
	region := flag.String("region", "", "AWS region for --s3 (falls back to the AWS default chain)")
	endpoint := flag.String("endpoint", "", "optional S3-compatible endpoint for --s3")
	forcePathStyle := flag.Bool("force-path-style", false, "use path-style S3 requests for --s3")
	flag.Parse()
	if *archive == "" && *directory == "" && *s3URI == "" {
		fmt.Fprintln(os.Stderr, "usage: kouja-restore (--archive FILE | --dir DIR | --s3 s3://bucket/prefix/) [--key-file FILE] [--config PATH] [--apply]")
		os.Exit(2)
	}
	if *apply {
		fmt.Fprintln(os.Stderr, "The agent must be stopped before --apply. Existing files may be replaced.")
	}
	if err := restore.Run(context.Background(), restore.Options{
		Archive: *archive, Directory: *directory, S3URI: *s3URI, KeyFile: *keyFile,
		ConfigPath: *configPath, Apply: *apply, Stdout: os.Stdout,
		Region: *region, Endpoint: *endpoint, ForcePathStyle: *forcePathStyle,
	}); err != nil {
		log.Fatal(err)
	}
}
