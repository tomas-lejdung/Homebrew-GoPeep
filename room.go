package main

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
}

var rng *rand.Rand

func init() {
	rng = rand.New(rand.NewSource(time.Now().UnixNano()))
}

// GenerateRoomCode creates a memorable room code in ADJECTIVE-NOUN-NN format
func GenerateRoomCode() string {
	adj := adjectives[rng.Intn(len(adjectives))]
	noun := nouns[rng.Intn(len(nouns))]
	num := rng.Intn(100)
	return fmt.Sprintf("%s-%s-%02d", adj, noun, num)
}

// NormalizeRoomCode ensures consistent formatting (uppercase, trimmed)
func NormalizeRoomCode(code string) string {
	return strings.ToUpper(strings.TrimSpace(code))
}

// ValidateRoomCode checks if a room code has valid format
func ValidateRoomCode(code string) bool {
	parts := strings.Split(code, "-")
	if len(parts) != 3 {
		return false
	}
	// Basic validation - could be more strict
	return len(parts[0]) > 0 && len(parts[1]) > 0 && len(parts[2]) > 0
}
