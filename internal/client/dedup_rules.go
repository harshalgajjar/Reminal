// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import "os"

// dedupStrict picks the rules a join's history is cleared of reprints by.
//
// Off (the default): any paragraph whose words are all covered by 8-word runs
// of the text kept before it, then after it, goes, and paragraphs printed
// again verbatim go too (dedupRepaintsCovered). It clears the most reprints;
// now and then it takes a line the program really printed twice.
//
// On (REMINAL_DEDUP=strict in the agent's environment): only a paragraph
// that can be a reprint (inside a resize's window, matching the frame it
// captured) is looked at, it must be one unbroken run of kept text, a
// command and its output are never a copy, and a run printed at two widths
// is split where the width changes (dedupRepaintsIn). It never takes a line
// a program printed again; some reprints stay as duplicates.
var dedupStrict = dedupStrictIn(os.Getenv)

// dedupStrictIn reads the choice from an environment.
func dedupStrictIn(getenv func(string) string) bool { return getenv("REMINAL_DEDUP") == "strict" }

// dedupRepaintsCovered is the default rules' pass (dedupStrict off).
func dedupRepaintsCovered(lines []string) []string {
	paras := dedupParagraphs(lines, false)
	// The verbatim copies first (dedupBlocks' rule), in the same mask.
	drop := dedupBlocksMask(lines)
	covered := func(p dedupPara, shingles map[shingle]bool, refWords map[string]bool) bool {
		whole := false
		for k, d := range coverageDropMask(lines[p.i:p.j], shingles, refWords) {
			if len(dedupWords(lines[p.i+k])) == 0 {
				continue
			}
			if !d {
				return false
			}
			whole = true
		}
		return whole
	}
	pass := func(order []int) {
		shingles := map[shingle]bool{}
		refWords := map[string]bool{}
		for _, k := range order {
			p := paras[k]
			if drop[p.i] {
				continue
			}
			if covered(p, shingles, refWords) {
				for x := p.i; x < p.j; x++ {
					drop[x] = true
				}
				continue
			}
			words := p.words(lines)
			addShingles(shingles, words)
			for _, w := range words {
				refWords[w] = true
			}
		}
	}
	forward := make([]int, len(paras))
	backward := make([]int, len(paras))
	for k := range paras {
		forward[k], backward[len(paras)-1-k] = k, k
	}
	pass(forward)
	pass(backward)
	dropOrphanHeadings(lines, paras, drop, false)
	dropHeadingDebris(lines, paras, drop, false)
	return compactDropped(lines, drop)
}
