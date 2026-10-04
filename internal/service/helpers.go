package service

import (
	"crypto/rand"
	"math/big"
	"sort"
)

func random6() int64 {
	n, _ := rand.Int(rand.Reader, big.NewInt(1000000))
	return n.Int64()
}

func sortInts(a []int) { sort.Ints(a) }
