package models

import (
	"fmt"
	"math/rand/v2"
	"time"
)

// Name pools for the "about you" step (app.py:465-472).
var (
	firstNames = []string{
		"Ethan", "Noah", "Liam", "Mason", "Lucas", "Logan", "Owen", "Ryan", "Leo", "Adam",
		"Ella", "Ava", "Mia", "Luna", "Chloe", "Grace", "Ruby", "Nora", "Ivy", "Sofia",
	}
	lastNames = []string{
		"Smith", "Brown", "Taylor", "Walker", "Wilson", "Clark", "Hall", "Young", "Allen", "King",
		"Scott", "Green", "Baker", "Adams", "Turner",
	}
)

// RandomProfile mirrors random_profile: a random "First Last" name and an ISO
// birthdate (YYYY-MM-DD) for an age of 25-34. The year window (~now-25..now-34)
// must stay inside the about-you validation range 1950..2007.
func RandomProfile() (name string, birthdate string) {
	age := randInt(25, 34)
	year := time.Now().UTC().Year() - age
	month := randInt(1, 12)
	day := randInt(1, 28)
	name = firstNames[rand.IntN(len(firstNames))] + " " + lastNames[rand.IntN(len(lastNames))]
	birthdate = fmt.Sprintf("%04d-%02d-%02d", year, month, day)
	return name, birthdate
}
