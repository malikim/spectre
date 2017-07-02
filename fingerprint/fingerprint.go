package fingerprint

import (
	_ "crypto/sha1"
	"fmt"
	"sort"
	"github.com/snuffpuppet/spectre/pcm"
	"github.com/snuffpuppet/spectre/analysis"
	"math"
	"log"
	"crypto/sha1"
	"io"
)

const REQUIRED_CANDIDATES = 4 		// required number of frequency candidates for a fingerprint entry
const LOWER_FREQ_CUTOFF = 0.0		// Lowest frequency acceptable for matching
//const LOWER_POWER_CUTOFF = 0.5
const LOWER_POWER_CUTOFF = 100		// Power levels below this amount are ignored for matching
const TIME_DELTA_THRESHOLD = 0.2	// required minimum time diff between freq matches to be considered a hit

const (
	_ = iota
	SA_PWELCH = iota
	SA_BESPOKE = iota
)


/*
 * Spectral Analysis and fingerprinting:
 *
 */
type candidate struct { Freq float64
			Pxx float64
}

type candidates []candidate
func (c candidates) String() string {
	var s string
	for _, v := range c {
		s += fmt.Sprintf("%9.2f (%.2f)\t", v.Freq, v.Pxx)
	}
	return s
}

type ByPxx []candidate
func (a ByPxx) Len() int           { return len(a) }
func (a ByPxx) Swap(i, j int)      { a[i], a[j] = a[j], a[i] }
func (a ByPxx) Less(i, j int) bool { return a[i].Pxx < a[j].Pxx }

type ByFreq []candidate
func (a ByFreq) Len() int           { return len(a) }
func (a ByFreq) Swap(i, j int)      { a[i], a[j] = a[j], a[i] }
func (a ByFreq) Less(i, j int) bool { return a[i].Freq < a[j].Freq }

// The data that the fingerprint maps to
type Mapping struct {
	Filename    string
	Timestamp   float64
}

// Fingerprint info on a block of audio data
type Fingerprint struct {
	Key           []byte
	Timestamp     float64
	Candidates    candidates
	Transcription Transcription
}

// For the Chroma identification method of matching:
// ref: http://musicweb.ucsd.edu/~sdubnov/CATbox/Reader/ThumbnailingMM05.pdf
const (
	A_NOTE = iota
	AS_NOTE = iota
	B_NOTE = iota
	C_NOTE = iota
	CS_NOTE = iota
	D_NOTE = iota
	DS_NOTE = iota
	E_NOTE = iota
	F_NOTE = iota
	FS_NOTE = iota
	G_NOTE = iota
	GS_NOTE = iota
	MAX_NOTE = iota
)

type note int

func (n note) String() (s string) {
	switch int(n) {
	case A_NOTE:
		s = "A"
	case AS_NOTE:
		s = "A#"
	case B_NOTE:
		s = "B"
	case C_NOTE:
		s = "C"
	case CS_NOTE:
		s = "C#"
	case D_NOTE:
		s = "D"
	case DS_NOTE:
		s = "D#"
	case E_NOTE:
		s = "E"
	case F_NOTE:
		s = "F"
	case FS_NOTE:
		s = "F#"
	case G_NOTE:
		s = "G"
	case GS_NOTE:
		s = "G#"
	default:
		log.Panicf("Unrecognised note enumertation %d", n)

	}

	return
}

// Logn(s^(1/12)) - used for Equal Tempered scale measurement in equalTempSteps function
const LOGNA = 0.05776226504666185940

// calculate the number of note semitones that the frequency is away from the 440Hz base tone
// Using the Equal Tempered Scale with A4 = 440Hz
//ref: http://www.phy.mtu.edu/~suits/notefreqs.html
func noteSteps(freq float64) float64 {
	return math.Log(freq/440.0)/ LOGNA
}

// find out to which note this frequency corresponds. Returns a number between 0 and 11
func freqNote(freq float64) int {
	n := int(noteSteps(freq) + 0.5) % MAX_NOTE
	if n < 0 {
		n += MAX_NOTE
	}

	return n
}

// A bucket of frequencies that make up a musical note
type Chroma struct {
	Note     note
	Freq     float64
	Strength float64
}

type Transcription []Chroma

func (t Transcription) String() string {
	s := ""
	for i, v := range t {
		s += fmt.Sprintf("[%s] %6.1f ", note(i), v.Freq)
	}

	return s
}

// Apply an approximation to the frequency to help with inacuracies with matching later
func fuzzyFreq(f float64) float64 {
	return float64(int(f/10 + 0.5)*10)
	//fuzzyFreq -= fuzzyFreq%2

}

// Convert the frequency/power data into buckets of musical notes based on strength of signal
func transcribe(freqs, Pxx []float64) (t Transcription) {
	chromaCount := 0
	t = make([]Chroma, MAX_NOTE)
	for i, v := range freqs {
		n := freqNote(v)
		if Pxx[i] > t[n].Strength {
			if Pxx[i] > LOWER_POWER_CUTOFF && v > LOWER_FREQ_CUTOFF {
				//log.Printf("*** Set %d(%s) -> %.1f(%.1f)\n", n, note(n), v, Pxx[i])
				t[n].Note = note(n)
				t[n].Freq = fuzzyFreq(v)
				t[n].Strength = Pxx[i]
				chromaCount++
			} else {
				//fmt.Printf("*** Rejected: %f(%.2f)\n", fuzzyFreq(v), Pxx[i])
			}
		}

	}

	if chromaCount == 0 {
		t = nil
	}

	return
}

// Generate a fingerprint based on the musical transcription of the frequencies in the audio frame
func audioKey(t Transcription) (key []byte) {
	// The Powerkey method uses a scaled strength of each of the 12 notes to generate the key
	// The frequency hash method uses the strongest frequencies for each of the notes to create a hash
	optPowerKey := true

	if t == nil {
		return nil
	}

	key = make([]byte, len(t))

	maxPxx := 0.0
	if optPowerKey {
		for _, v := range t {
			if v.Strength > maxPxx {
				maxPxx = v.Strength
			}
		}
		for i, v := range t {
			key[i] = byte(int(v.Strength/maxPxx * 8.0 + 0.5))
		}
	} else {

		hash := sha1.New()

		for _, v := range t {
			io.WriteString(hash, fmt.Sprintf("%e", v.Freq))
		}

		key = hash.Sum(nil)
	}
	return
}

// return te strongest (REQUIRED_CANDIDATES) frequencies in the frequency data
func getTopCandidates(freqs, Pxx []float64) (candidates) {
	candidates := make([]candidate, 0)

	// select only those stronger than the power threshold and higher than the frequency threshold
	for i, v := range Pxx {

		if v > LOWER_POWER_CUTOFF && freqs[i] > LOWER_FREQ_CUTOFF {
			candidates = append(candidates, candidate{Freq: fuzzyFreq(freqs[i]), Pxx: v})
		}
	}

	// Sort the list in descending order
	sort.Sort(sort.Reverse(ByPxx(candidates)))

	var topCandidates []candidate
	if len(candidates) < REQUIRED_CANDIDATES {
		return nil
	}

	// Get the strongest signals
	topCandidates = candidates[:REQUIRED_CANDIDATES]

	// Sort by Frequency to adjust for any minor signal strength variance between them
	sort.Sort(sort.Reverse(ByFreq(topCandidates)))

	return topCandidates
}

// Use a basic frequency banding method for classifying frequencies and choosing candidates for the fingerprint
// Return the strongest frequency in each of four bands ordered by strength
func getBandedCandidates(freqs, Pxx []float64) (candidates) {

	candidates := make([]candidate, 0)
	highScores := make(map[int]float64)
	highPoints := make(map[int]float64)

	var freqBand = func(f float64) int {
		uLimit := 11025.0 / 2.0
		a := f - LOWER_FREQ_CUTOFF
		b := uLimit - LOWER_FREQ_CUTOFF

		x := int(a / b * 4 + 0.5)

		//fmt.Printf("%.2f => Band %d (a=%.2f, b=%.2f)\n", f, x, a, b)
		return x
	}

	// select only those stronger than the power threshold and higher than the frequency threshold
	for i, v := range Pxx {
		if v > LOWER_POWER_CUTOFF && freqs[i] > LOWER_FREQ_CUTOFF {
			fb := freqBand(freqs[i])
			if v > highScores[fb] {
				highPoints[fb] = freqs[i]
				highScores[fb] = v
			}
		}
	}

	for k, v := range highPoints {
		candidates = append(candidates, candidate{Freq: fuzzyFreq(v), Pxx: highScores[k]})
	}

	// Sort by Frequency to adjust for any minor signal strength variance between them
	sort.Sort(sort.Reverse(ByFreq(candidates)))

	return candidates
}

/*
func PrintCandidates(blockId int, blockTime float64, candidates []candidate) {
	s := ""
	for _, v := range candidates {
		//f += fmt.Sprintf("%9.2f", v.Freq)
		//p += fmt.Sprintf("%9.4f", v.Pxx)
		s += fmt.Sprintf("%9.2f (%.2f)\t", v.Freq, v.Pxx)
	}
	//fmt.Printf("[%4d:%6.2f] %s\n              %s\n", sampleBlock.Id, sampleBlock.Timestamp, f, p)
	fmt.Printf("\t[%4d:%6.2f] %s\n", blockId, blockTime, s)
}
*/

// log some frequency distribution data for the given spectrum
func logSamples(verbose bool, freqs, Pxx []float64) {
	var top, bottom, avg, topf, bottomf float64
	var count int

	if !verbose {
		return
	}

	bottom = -1.0
	for i, x := range Pxx {
		if x > LOWER_POWER_CUTOFF && freqs[i] > LOWER_FREQ_CUTOFF {
			if x > top {
				top = x
				topf = freqs[i]
			}
			if x < bottom {
				bottom = x
				bottomf = freqs[i]
			}
			avg += x
			count++
		}
	}

	if count > 0 {
		log.Printf("#S:%3d T: [%7.1f] %7.1f\tB: [%7.1f] %7.1f\tA: %7.1f", count, topf, top, bottomf, bottom, avg / float64(len(Pxx)))
	}
}

func New(sampleBlock *pcm.Buffer, sampleRate int, optSpectralAnalyser int, optVerbose bool) (*Fingerprint) {
	var Pxx, freqs []float64
	switch optSpectralAnalyser {
	case SA_PWELCH:
		Pxx, freqs = analysis.PwelchAnalysis(sampleBlock, sampleRate)
	case SA_BESPOKE:
		Pxx, freqs = analysis.OverlapAnalysis(sampleBlock, sampleRate)
	default:
		log.Panicf("Unrecognised spectral analyser %d\n", optSpectralAnalyser)
	}

	optMethod :=  "transcribe" //"freqbands" // "transcribe", "topfreq"

	//logSamples(optVerbose, freqs, Pxx)

	var key []byte
	var fp Fingerprint

	switch (optMethod) {
	case "transcribe":
		transcription := transcribe(freqs, Pxx)
		//log.Printf("fp transscription: %s\n", transcription)

		key := audioKey(transcription)
		//log.Printf("fp key: %s\n", key)

		if key == nil {
			return nil
		}
		fp = Fingerprint{
			Key: key,
			Timestamp: sampleBlock.Timestamp,
			Candidates: nil,
			Transcription: transcription,
		}
	case "topfreqs":
		candidates := getTopCandidates(freqs, Pxx)

		if len(candidates) < REQUIRED_CANDIDATES {
			return nil        // no valid candidates
		}

		// Now copy over the ones that we are interested in and populate the hash string
		hash := sha1.New()
		for _, v := range candidates {
			io.WriteString(hash, fmt.Sprintf("%e", v.Freq))
		}

		key = hash.Sum(nil)

		fp = Fingerprint{
			Key: key,
			Timestamp: sampleBlock.Timestamp,
			Candidates: candidates,
			Transcription: nil,
		}
	case "freqbands":
		candidates := getBandedCandidates(freqs, Pxx)

		if len(candidates) < REQUIRED_CANDIDATES {
			return nil        // no valid candidates
		}

		// Now copy over the ones that we are interested in and populate the hash string
		hash := sha1.New()
		for _, v := range candidates {
			io.WriteString(hash, fmt.Sprintf("%e", v.Freq))
		}

		key = hash.Sum(nil)

		fp = Fingerprint{
			Key: key,
			Timestamp: sampleBlock.Timestamp,
			Candidates: candidates,
			Transcription: nil,
		}
	default:
		log.Panicf("Fingerprint: Unknown key generaion method: %s", optMethod)
	}


	return &fp
}

