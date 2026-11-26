package main

import (
	"log"
	"os"
	"path/filepath"
	"strings"

	"gpupanel/internal/models"
)

// FFmpeg 9.1 was bundled in 1.0.7 and broke inputs; remove it and fall back to the system FFmpeg.
const ffBundleDir = "/opt/mastergpu/ffmpeg/9.1"

func removeFFmpegBundle(set *models.Settings) {
	if strings.HasPrefix(set.FFmpegPath, ffBundleDir) {
		set.FFmpegPath, set.FFprobePath = "ffmpeg", "ffprobe"
	}
	if !fileExists(ffBundleDir) {
		return
	}
	if err := os.RemoveAll(ffBundleDir); err != nil {
		log.Printf("remove %s: %v", ffBundleDir, err)
		return
	}
	log.Printf("removed bundled FFmpeg %s", ffBundleDir)
	os.Remove(filepath.Dir(ffBundleDir))
}
