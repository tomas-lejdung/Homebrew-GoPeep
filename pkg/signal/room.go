package signal

import (
	"fmt"
	"math/rand"
	"strings"
	"time"
)

var adjectives = []string{
	"QUICK", "LAZY", "HAPPY", "CALM", "BRAVE",
	"BRIGHT", "COOL", "DARK", "EAGER", "FAIR",
	"GENTLE", "GRAND", "GREAT", "GREEN", "BLUE",
	"RED", "GOLD", "SILVER", "WARM", "WILD",
	"BOLD", "CLEAN", "CLEAR", "CRISP", "DEEP",
	"FAST", "FINE", "FRESH", "GOOD", "HIGH",
	"KIND", "LIGHT", "LOUD", "MILD", "NEAT",
	"NICE", "PLAIN", "PROUD", "PURE", "RICH",
	"SAFE", "SHARP", "SLIM", "SMART", "SOFT",
	"SWEET", "TALL", "TRUE", "VAST", "WISE",
	"AGILE", "ALERT", "AMPLE", "AZURE", "BASIC",
	"BITTER", "BLAZING", "BREEZY", "BRIEF", "BRONZE",
	"BUSY", "COSMIC", "COZY", "DARING", "DENSE",
	"DIVINE", "DOUBLE", "DUSTY", "EARLY", "EASY",
	"ELITE", "EMPTY", "EPIC", "EVEN", "EXOTIC",
	"FANCY", "FATAL", "FOGGY", "FREE", "FROZEN",
	"GIANT", "GLOSSY", "GOLDEN", "GRASSY", "GRIM",
	"HASTY", "HEAVY", "HOLLOW", "HUMBLE", "ICED",
	"INNER", "IVORY", "JOLLY", "JUMPY", "KEEN",
	"LIVELY", "LONE", "LUCKY", "MAJOR", "MELLOW",
}

var nouns = []string{
	"FROG", "TIGER", "RIVER", "CLOUD", "STONE",
	"LEAF", "BIRD", "FISH", "WOLF", "BEAR",
	"HAWK", "DEER", "LION", "EAGLE", "WHALE",
	"PANDA", "KOALA", "OTTER", "SNAKE", "SHARK",
	"TREE", "LAKE", "MOON", "STAR", "WAVE",
	"WIND", "FLAME", "FROST", "PEAK", "CAVE",
	"DAWN", "DUSK", "MIST", "RAIN", "SNOW",
	"STORM", "BEACH", "CLIFF", "DELTA", "GROVE",
	"HILL", "MARSH", "MESA", "OASIS", "PLAIN",
	"RIDGE", "SHORE", "TRAIL", "VALE", "WOODS",
	"ANCHOR", "BADGE", "BANNER", "BEACON", "BLADE",
	"CANYON", "CASTLE", "CEDAR", "COMET", "CORAL",
	"CREEK", "CROWN", "DESERT", "DOME", "ECHO",
	"EMBER", "FALCON", "FERRY", "FIELD", "FLINT",
	"FORGE", "GARDEN", "GATE", "GLACIER", "HARBOR",
	"HERON", "ISLAND", "JEWEL", "JUNGLE", "LOTUS",
	"MEADOW", "METEOR", "ORBIT", "PALM", "PEARL",
	"PHOENIX", "PINE", "PRISM", "QUARTZ", "RAVEN",
	"REEF", "ROSE", "SAGE", "SEAL", "SHADOW",
	"SOLAR", "TEMPLE", "THUNDER", "TUNDRA", "VAPOR",
}

// Password words - simple, memorable words for password generation
var passwordWords = []string{
	"tiger", "apple", "river", "cloud", "stone",
	"flame", "ocean", "piano", "robot", "honey",
	"grape", "lemon", "maple", "north", "solar",
	"storm", "zebra", "delta", "omega", "lunar",
	"coral", "frost", "bloom", "spark", "wave",
}

var rng *rand.Rand

func init() {
	rng = rand.New(rand.NewSource(time.Now().UnixNano()))
}

// GenerateRoomCode creates a memorable room code in ADJECTIVE-NOUN-NNN format
func GenerateRoomCode() string {
	adj := adjectives[rng.Intn(len(adjectives))]
	noun := nouns[rng.Intn(len(nouns))]
	num := rng.Intn(1000)
	return fmt.Sprintf("%s-%s-%03d", adj, noun, num)
}

// NormalizeRoomCode ensures consistent formatting (uppercase, trimmed)
func NormalizeRoomCode(code string) string {
	return strings.ToUpper(strings.TrimSpace(code))
}

// ValidateRoomCode checks if a room code has valid format (ADJECTIVE-NOUN-NN or ADJECTIVE-NOUN-NNN)
func ValidateRoomCode(code string) bool {
	parts := strings.Split(code, "-")
	if len(parts) != 3 {
		return false
	}
	// Validate each part is non-empty and number part is 2-3 digits
	if len(parts[0]) == 0 || len(parts[1]) == 0 {
		return false
	}
	numPart := parts[2]
	if len(numPart) < 2 || len(numPart) > 3 {
		return false
	}
	for _, c := range numPart {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// GeneratePassword creates a memorable password in word-NN format (e.g., "tiger-42")
func GeneratePassword() string {
	word := passwordWords[rng.Intn(len(passwordWords))]
	num := rng.Intn(100)
	return fmt.Sprintf("%s-%02d", word, num)
}
