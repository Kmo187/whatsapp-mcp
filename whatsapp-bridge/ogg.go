package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math"
	"math/rand"
)

// analyzeOggOpus reads the duration of an Ogg Opus file and returns a
// waveform for the voice-note preview.
func analyzeOggOpus(data []byte) (duration uint32, waveform []byte, err error) {
	if len(data) < 4 || string(data[0:4]) != "OggS" {
		return 0, nil, fmt.Errorf("not a valid Ogg file (missing OggS signature)")
	}

	var lastGranule uint64
	var preSkip uint16
	foundHead := false

	for i := 0; i < len(data); {
		if i+27 > len(data) {
			break
		}
		if string(data[i:i+4]) != "OggS" {
			i++
			continue
		}
		granulePos := binary.LittleEndian.Uint64(data[i+6 : i+14])
		pageSeq := binary.LittleEndian.Uint32(data[i+18 : i+22])
		numSegments := int(data[i+26])
		if i+27+numSegments > len(data) {
			break
		}
		pageSize := 27 + numSegments
		for _, segLen := range data[i+27 : i+27+numSegments] {
			pageSize += int(segLen)
		}
		end := i + pageSize
		if end > len(data) {
			end = len(data)
		}
		if !foundHead && pageSeq <= 1 {
			page := data[i:end]
			if pos := bytes.Index(page, []byte("OpusHead")); pos >= 0 && pos+16 <= len(page) {
				// OpusHead: magic(8) version(1) channels(1) preSkip(2) sampleRate(4)
				preSkip = binary.LittleEndian.Uint16(page[pos+10 : pos+12])
				foundHead = true
			}
		}
		// granule -1 (all bits set) means "no packet ends on this page"
		if granulePos != 0 && granulePos != math.MaxUint64 {
			lastGranule = granulePos
		}
		i += pageSize
	}

	// Opus granule positions always count 48 kHz samples, regardless of the input rate in OpusHead.
	const opusClock = 48000.0
	if lastGranule > uint64(preSkip) {
		duration = uint32(math.Ceil(float64(lastGranule-uint64(preSkip)) / opusClock))
	} else {
		duration = uint32(float64(len(data)) / 2000.0)
	}
	if duration < 1 {
		duration = 1
	} else if duration > 3600 {
		duration = 3600
	}
	return duration, placeholderWaveform(duration), nil
}

// placeholderWaveform generates a deterministic, natural looking 64-sample
// waveform (values 0-100) for voice messages.
func placeholderWaveform(duration uint32) []byte {
	const n = 64
	waveform := make([]byte, n)
	rng := rand.New(rand.NewSource(int64(duration)))
	base := 35.0
	freq := float64(min(int(duration), 120)) / 30.0
	for i := range waveform {
		pos := float64(i) / float64(n)
		val := base*math.Sin(pos*math.Pi*freq*8) + (base/2)*math.Sin(pos*math.Pi*freq*16)
		val += (rng.Float64() - 0.5) * 15
		val = val*(0.7+0.3*math.Sin(pos*math.Pi)) + 50
		waveform[i] = byte(math.Max(0, math.Min(100, val)))
	}
	return waveform
}
