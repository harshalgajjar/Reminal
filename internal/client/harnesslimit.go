// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// Whether the agent in this session has run out of what its service lets it
// use: a usage limit, a rate limit, a quota. A harness at its limit takes a
// message, answers it with "Usage limit reached · resets 1:30pm" and sits at
// its prompt: to everything that watches turns it is done, waiting for the
// next message. Every message sent it goes the same way until the limit
// resets, and nothing comes back by itself once it has: someone has to send
// it on.
//
// So, as for a login (harnesshealth.go), the bottom of the screen is read on
// every attention tick, and while it carries the harness's own limit notice
// the session reports harnessLimited, and when the notice says, the time the
// limit resets. Detection only: nothing is typed.

// harnessLimited is the state a session reports while its agent is held back
// by its service's limit. Like harnessLoggedOut it says the agent can do
// nothing now, and outranks what it looked like it was doing.
const harnessLimited = "limited"

// limitNotice is one harness's limit notice: what its row reads as from its
// first letter or digit, lowercase, all whitespace removed (as the login
// notices are read: Claude Code draws its spaces as cursor movements).
type limitNotice struct {
	re *regexp.Regexp
	// whole: the notice is its row's to its end — what follows its words is
	// only when it resets, nothing more — read with the rows it wraps onto.
	// A sentence that starts with its words and goes on is an agent's own
	// (review): "You've hit your session limit · resets 3pm — the agent
	// now waits."
	whole bool
	// glyph, if set, are the marks one of which the row must lead with. Words
	// plain enough to end anyone's sentence ("out of usage credits") are the
	// notice only on the row Claude Code draws a notice on (⎿): a wrapped
	// answer's last row has no glyph, nor does a narrow screen's second row
	// of "Fast mode disabled · Out of usage credits" (review).
	glyph string
	// cased, if set, is how the notice's words are written, capitals and
	// all, which the row must show: Antigravity's "AI: Out of credits", not
	// a sentence's "AI: out of credits" (review).
	cased string
	// modelLead: the notice may start with a model's id, in lower case as
	// the CLI writes it ("gpt-5 usage limit reached."): a whole notice's
	// capital-first rule then allows a row that starts with one (review).
	modelLead bool
	// last: the notice is its answer's last row. Under the rows it took, up
	// to the prompt, only blank rows, a box rule, a turn's time line or its
	// hint: an answer or a tool's output that quotes a notice drawn as one
	// (●, ⎿) and goes on is not it (review).
	last bool
}

// modelLed is ns, each allowed to start with a model's id in lower case.
func modelLed(ns []limitNotice) []limitNotice {
	for i := range ns {
		ns[i].modelLead = true
	}
	return ns
}

// modelLeadRe is a row, as drawn, that starts with a hyphenated word and a
// space: a model's id, if modelLeads says so.
var modelLeadRe = regexp.MustCompile(`^[a-z][a-z0-9.]{0,11}(?:-[a-z0-9.]{1,12}){1,5}\s`)

// modelVendorRe opens a model's id that may have no digit in it: a known
// maker's, or one OpenCode serves under a name of its own ("big-pickle").
var modelVendorRe = regexp.MustCompile(`^(?:gpt|claude|gemini|qwen|o\d|llama|mistral|deepseek|grok|kimi|glm|codestral|devstral|big-pickle)`)

// modelLeads reports whether row, as drawn, starts with a model's id: a
// hyphenated word with a digit in it, or one modelVendorRe knows ("gpt-5",
// "claude-opus-4", "big-pickle"); never a word like "re-run" (review).
func modelLeads(row string) bool {
	m := modelLeadRe.FindString(row)
	return m != "" && (strings.ContainsAny(m, "0123456789") || modelVendorRe.MatchString(m))
}

// asLast is ns, each counted only as its answer's last row.
func asLast(ns []limitNotice) []limitNotice {
	for i := range ns {
		ns[i].last = true
	}
	return ns
}

// onCased is ns, each matched only on a row that writes lit as it is.
func onCased(lit string, ns []limitNotice) []limitNotice {
	for i := range ns {
		ns[i].cased = lit
	}
	return ns
}

// onGlyph is ns, each matched only on a row led by one of glyph's marks.
func onGlyph(glyph string, ns []limitNotice) []limitNotice {
	for i := range ns {
		ns[i].glyph = glyph
	}
	return ns
}

func notices(res ...string) []limitNotice {
	out := make([]limitNotice, len(res))
	for i, r := range res {
		out[i] = limitNotice{re: regexp.MustCompile("^(?:" + r + ")")}
	}
	return out
}

// limitModel is a model's name where a notice fills one in, as its row reads
// flattened: short parts joined by hyphens ("gemini-2.5-pro", "gpt-5"), so
// an agent's own words after it, or a sentence's next row, are never part
// of it.
const limitModel = `[a-z][a-z0-9.]{0,11}(?:-[a-z0-9.]{1,12}){1,5}`

// limitWait is how long a notice says to wait, flattened: "3hours20minutes",
// "45s", "2h5m".
const limitWait = `(?:\d+(?:days?|hours?|hrs?|minutes?|mins?|seconds?|secs?|d|h|m|s),?(?:and)?)+`

// claudeModel is a Claude model a notice names, perhaps with its version.
const claudeModel = `(?:claude|opus|sonnet|haiku|fable)?[0-9.]*`

// limitCommand is the command a notice may end on, as its row reads with
// spaces gone and in lower case: "· /usage-credits to keep using Claude",
// "· run /usage-credits to ask your admin for more" (or "for a higher
// limit"), "· /usage-credits to request more usage from your admin",
// ". Run /usage-credits to raise it and keep using Claude or switch models
// to continue this chat". Each of its words is one the notice uses.
const limitCommand = `(?:[·.](?:run)?/usage-credits(?:tokeepusing` + claudeModel +
	`|toaskyouradminfor(?:more|ahigherlimit)|torequestmoreusagefromyouradmin|torequestthisfromyouradmin` +
	`|toadjust|toraisethecap|tomanageyourlimitandkeepusing` + claudeModel + `|toturnonextrausageforyourorg` +
	`|tocontinueorswitchmodelswith/model|toraiseitandkeepusing` + claudeModel + `(?:orswitchmodelstocontinuethischat)?)?)?`

// claudeEnd is what may follow a Claude Code notice's words on its row:
// perhaps when it resets ("· resets 3pm (America/Los_Angeles)", "resets at
// 1:30pm"), "· progress saved", and the command it points to.
const claudeEnd = `(?:·?(?:limit)?resets?(?:at)?` + limitWhen + `)?(?:·progresssaved)?` + limitCommand + `\.?`

// claudeEndTold is claudeEnd with a reset time or a command in it, not
// optional: what a notice Claude Code draws as an answer (●) always carries,
// and an answer's own sentence ("● You've hit your session limit.") does not.
var claudeEndTold = `(?:(?:·?(?:limit)?resets?(?:at)?` + limitWhen + `)(?:·progresssaved)?` + limitCommand +
	`|(?:·progresssaved)?` + strings.TrimSuffix(limitCommand, "?") + `)\.?`

// claudeHitLimit are Claude Code's "You've hit your … limit" notices (a
// daily or an individual spend limit too, a team's, a channel's), and its
// organization out of usage: counted on its notice row (⎿), or drawn as an
// answer (●) only with the reset time or the command a notice carries.
var claudeHitLimit = []string{
	`you.?vehityour(?:session|weekly|daily|monthly|opus|sonnet|fable|usagecredit|usage|monthlyspend|individualspend|spend|fast)?limit`,
	`you.?vehityour(?:team|org).?s(?:shared|monthly)[a-z]{0,16}(?:limit|budget)`,
	`you.?vehityourchannel.?smonthly[a-z]{0,16}(?:limit|budget)`,
	`yourorgisoutofusage(?:credits)?(?:·(?:addfundstocontinue|contactyouradmin))?`,
}

// wholeRows are notices read as whole rows: each's words, then only what
// end allows, then the row's end; read with the rows it wraps onto. A row
// that goes on in words of its own is a sentence, not the notice (an
// agent's answer that quotes the notice and goes on is not the notice), so
// end is only ever words the notice itself uses, never "anything after".
func wholeRows(end string, res ...string) []limitNotice {
	out := make([]limitNotice, len(res))
	for i, r := range res {
		out[i] = limitNotice{re: regexp.MustCompile("^(?:" + r + ")" + end + "$"), whole: true}
	}
	return out
}

// wholeNotices are Claude Code's notices, read as whole rows (claudeEnd).
func wholeNotices(res ...string) []limitNotice { return wholeRows(claudeEnd, res...) }

// limitWhen is a reset time as a notice writes it, flattened: perhaps a date
// ("oct21,"), a clock time, perhaps a zone.
const limitWhen = `(?:on)?(?:(?:jan|feb|mar|apr|may|jun|jul|aug|sep|oct|nov|dec)[a-z]*\d{1,2}(?:st|nd|rd|th)?,?(?:\d{4})?(?:at)?)?\d{1,2}(?::\d{2})?(?:am|pm)?(?:\([a-z0-9_+\-/]+\)|utc|gmt|[pmce][sd]t|bst|cet|cest)?`

// codexEnd is what may follow a Codex notice: when to try again.
const codexEnd = `(?:,?ortryagain(?:later|at` + limitWhen + `))?\.?`

// harnessLimitNotices are, by CLI, the rows that say it is at its limit —
// its own words, matched from the row's start (after the glyphs it draws
// before them: "⎿", "●", "■", "✕") to the row's end. A sentence that quotes
// them is not them, nor is a row at the input prompt: a person's own text,
// or the conversation repeating what a person typed, starts with the
// prompt's ">" or "›". Words plain enough to end anyone's sentence ("rate
// limit exceeded") count only on the row the CLI draws its notice on, led
// by its glyph; where that glyph is not known, they are not read at all.
//
// What each entry was checked against — a screen captured from the CLI
// itself, or only its source — is in harnesslimit_test.go.
var harnessLimitNotices = map[string][]limitNotice{
	// Claude Code 2.1.303: "You've hit your session limit · resets 3pm
	// (America/Los_Angeles)" — session, weekly, Opus, Sonnet, usage credit,
	// monthly spend, fast; "· progress saved" after it. Its team's or org's
	// budget, credits run out, and the plain headings. Whole rows: what is
	// read here may be acted on, so a sentence that only starts with them is
	// never one.
	"claude": append(append(append(onGlyph("⎿", asLast(wholeNotices(claudeHitLimit...))), onGlyph("●", asLast(wholeRows(claudeEndTold, claudeHitLimit...)))...), wholeNotices(
		// Claude Code 2.1.312: what to do next after it ("You're out of
		// usage credits. /model to switch models.").
		`you.?reoutofusagecredits(?:\.(?:/modeltoswitchmodels|switchtoanothermodel))?`,
		// Claude Code 2.1.312: a limit that could not be lifted, and the
		// automatic continue that gave up. Each with its own row ending only.
		// ("Fast mode disabled · Out of usage credits" is not one: fast mode
		// goes off and the session carries on; nor is "Continue now at lower
		// priority …", an option that keeps it going.)
		`couldn.?tresetyour5-hourlimitwiththislogin(?:·run/login,thentryagain)?`,
		`a5-hourlimitresetisn.?tavailablerightnow`,
		`automaticcontinuestoppedafterrepeatedusagelimithits(?:·thistaskwillnotresumeonitsown\(/rate-limit-optionstotryagain\))?`,
		`paused·usagelimit`,
		`fastlimitreachedandtemporarilydisabled`,
		// Claude Code 2.1.317: a goal paused at its limit, waiting for a
		// message after the reset or going on by itself then.
		`goalpaused·usagelimitreached(?:·sendamessageafteritresetstocontinue|·continuesautomaticallywhenitresets)?`,
		// Claude Code 2.1.317: a group's pooled budget on the Claude apps
		// gateway, with the day and time it resets (UTC).
		`thisrequestexceedsyourgroup.?sbudget\.askyourorganization.?sadminstoraiseit,ortryagainafteritresetson`+limitWhen,
	)...),
		// Bare words that could end any sentence: only on the notice's own row.
		// "Usage limit reached · continuing automatically at 3pm · esc to
		// cancel" is at its limit too: held until then, as any notice with a
		// time is, and going on by itself (not "Fable limit reached ·
		// continuing on Opus …", which goes on now, on another model).
		onGlyph("⎿", asLast(wholeNotices(
			`outofusagecredits`,
			`usagelimitreached(?:again)?(?:·continuingautomatically(?:at`+limitWhen+`|in`+limitWait+`)?·esctocancel)?`,
			`(?:fable|opus|sonnet)limitreached`,
			// Claude Code 2.1.312: extra usage, a model's own limit, and its
			// organization's — words an answer may say too (review).
			`you.?reoutofextrausage`,
			`you.?vereachedyour(?:session|weekly|daily|monthly|opus|sonnet|fable|usage)?limit`,
			`yourorganizationisoutofusagecredits(?:\.contactyouradmintoaddmore)?`,
		)))...),
	// Codex 0.163–0.165: "You’ve hit your usage limit. … try again at 3:05
	// PM." (the time on the next row when it wraps), its workspace's limit
	// and credits, its included usage, and the service's quota. Each with
	// the words it goes on with, and only them.
	"codex": wholeRows(codexEnd,
		`you.?vehityourusagelimit(?:for`+limitModel+`)?(?:\.(?:upgradetoplustocontinueusingcodex\(https://chatgpt\.com/explore/plus\)`+
			`|upgradetopro\(https://chatgpt\.com/explore/pro\),visithttps://chatgpt\.com/settings/usagetopurchasemorecredits`+
			`|visithttps://chatgpt\.com/settings/usagetopurchasemorecredits|togetmoreaccessnow,sendarequesttoyouradmin))?`,
		`usagelimitreached\.you.?vereachedyourusagelimit`,
		`(?:you.?reoutofcredits\.)?yourworkspaceisoutofcredits\.(?:addcreditstocontinue(?:usingcodex)?`+
			`|askyourworkspaceownertoaddmore(?:\.notifyowner\?)?|askyourworkspaceownertorefillinordertocontinue)`,
		`you.?vereachedyourworkspacecreditlimit`,
		`yourincludedusageisexhausted(?:\.chooseanoptionbelowtocontinue)?`,
		// Codex 0.166: a workspace member at its limit, asked to request
		// more from its owner.
		`requestalimitincreasefromyourownertocontinueusingcodex(?:\.requestincrease\?)?`,
		`quotaexceeded\.checkyourplanandbillingdetails`,
	),
	// Gemini CLI 0.64–0.66: "Usage limit reached for gemini-2.5-pro.", then
	// "Access resets at 3:05 PM PDT." on a row of its own; its daily quota;
	// a rate limit, and what it says to do about one. Its errors are led by
	// "✕".
	"gemini": append(wholeRows(`\.?`,
		`usagelimitreachedfor`+limitModel,
		`youhaveexhaustedyourdailyquotaonthismodel`,
		`ratelimitexceeded\.tryagainlater`,
		`pleasewaitandtryagainlater\.toincreaseyourlimits,requestaquotaincreasethrough(?:aistudio|vertex),orswitchtoanother/authmethod`,
	), onGlyph("✕", wholeRows(`\.?`,
		`ratelimitexceeded`,
	))...),
	// Qwen Code 0.25–0.26: the service's 429 in its error line, and what it
	// adds; its own quota's and rate limit's words. Its errors are led by
	// "✕" (and "[API Error: …]").
	"qwen": append(wholeRows(`\.?`,
		`apierror:.{0,200}\(status:429\)\]`,
		`possiblequotalimitationsinplaceorslowresponsetimesdetected\.pleasewaitandtryagainlater`,
		`ratelimitexceeded\.(?:pleasewaitandtryagain|tryagainlater)`,
		`modelapiratelimitexceeded\(http429\)(?:\.waitandtryagainlater)?`,
		`qwenoauthquotaexceeded`,
	), onGlyph("✕", wholeRows(`\.?\]?`,
		`(?:apierror:)?quotaexhausted`,
		`(?:apierror:)?ratelimitexceeded`,
		// Qwen Code 0.26.9: its credits used up (the weekly ones too), with
		// what to do next, and when they come back; led by ✕ as its other
		// notices are.
		`you.?veusedupyour(?:weekly)?credits\.(?:pleasetryagainafterthereset|youcanwaitorrun/upgradetogetmorecredits)`,
		`yourcreditsareexpectedtoresetin`+limitWait,
	))...),
	// OpenCode 1.19: "Usage limit reached. It will reset in 3 hours 20
	// minutes." (a model's name may lead it), what to do now, its Go and
	// free plans', a rate limit.
	"opencode": append(modelLed(wholeRows(`\.?`,
		`(?:`+limitModel+`)?usagelimitreached\.(?:itwillresetin`+limitWait+`\.)?tocontinueusingthismodelnow,enableusagefromyouravailablebalance`,
		`(?:`+limitModel+`)?usagelimitreached\.itwillresetin`+limitWait,
	)), wholeRows(`\.?`,
		`golimitreached`,
		`freelimitreached`,
		`freeusageexceeded(?:,subscribetogo)?`,
		`ratelimitexceeded,retryin`+limitWait,
		`quotaexceeded\.checkyourplanandbillingdetails`,
	)...),
	// goose 1.55–1.56: its own words, inside "Ran into this error: …" (the
	// service's 429 after it) or bare where they are its own.
	"goose": wholeRows(`\.?`,
		`ranintothiserror:(?:ratelimitexceeded|creditsexhausted)(?::429toomanyrequests)?`,
		`(?:ranintothiserror:)?ratelimited—waitamomentandtryagain`,
		`creditsexhausted`,
		`pleaseaddcreditstoyouraccount,thenresendyourmessagetocontinue`,
	),
	// Crush 0.98 has no notice of its own: under "Provider Error" it shows
	// the service's message. OpenAI's and Anthropic's words for a limit.
	"crush": notices(
		`youexceededyourcurrentquota`,
		`ratelimitexceeded`,
		`(error:)?rate_limit_error`,
	),
	// Cursor CLI: the limit's words come from its service; the client's own,
	// for a usage or rate limit, are what to do about it.
	"cursor-agent": wholeRows(`\.?`,
		`upgradeyourplantocontinue`,
	),
	// Amp 0.0.1793131239: out of credits, what to do about it, its rate
	// limit and quota. ("You've reached your free usage limit" and "You've
	// exceeded your usage limit of $20" are words an answer may say too, and
	// the mark Amp draws before its notices is not known: not read.)
	"amp": wholeRows(`\.?`,
		`youareoutofcredits\.addpaidcreditsorchooseapaidtier`,
		`addcreditstokeepusingamp(?:rightnow,orwaituntilthenexthourstartsformorefreeusage)?`,
		`upgradetoapaidtier,oraddcreditstokeepusingamp`,
		`upgradetogigawattformoreincludedusage,oraddcreditstokeepusingamp`,
		`ratelimitexceeded\.tryagainshortly`,
		`usagequotaexceeded`,
	),
	// pi 1.4: its monthly limit, a ChatGPT plan's or workspace's, and a
	// Hugging Face rate limit with when to retry.
	"pi": wholeRows(`\.?`,
		`monthlyusagelimitreached`,
		`you(?:have|.?ve)hityourchatgptusagelimit`,
		`you.?vereachedyourchatgptworkspace.?susagelimit\.askyourworkspaceadmintoincreaseitatadmin\.openai\.com→workspace→usagelimits`,
		`huggingfaceratelimitreached(?:;retryin`+limitWait+`)?`,
	),
	// Copilot CLI 1.0.106: its own rate limits, a session's, a week's or a
	// model's, then what to do about one, and where to read more. And Copilot
	// Free's month of chat requests used up.
	"copilot": wholeRows(`\.?`,
		`you.?ve(?:hityour(?:session)?ratelimit|hittheratelimitforthismodel|reachedyourweeklyratelimit)`+
			`(?:\.(?:please(?:switchmodelsor)?waitforyourlimittoreset)(?:orswitchtoautomodeltocontinue)?)?`+
			`(?:\.?learnmore\(https://docs\.github\.com/copilot/concepts/rate-limits\))?`,
		`you.?vebeenratelimitedonthecurrentmodel\.wouldyouliketoswitchto[a-z0-9.\-]{1,40}tocontinue\?`,
		`you.?veusedallyourcopilotfreechatrequestsforthemonth`,
	),
	// Antigravity 1.4: out of credits, as its status row says it.
	"agy": onCased("AI: Out of credits", wholeRows(`\.?`,
		`ai:outofcredits`,
	)),
}

// limitPromptGlyphs are what an input prompt, or a person's message echoed
// in the conversation, starts with: a row that starts with one is a person's
// text, never the harness's notice.
const limitPromptGlyphs = ">›❯"

// limitQuoteGlyphs open a quote: a row that starts with one quotes a notice
// (an agent's answer, a test's string), never is one.
const limitQuoteGlyphs = "\"'`“”‘’«"

// limitProseGlyphs open a list item (Codex's "•" too), a heading or a table
// cell: an agent's own text, never a notice (review).
const limitProseGlyphs = "-*#|•"

// limitAnswerGlyphs open what answers a message: Claude Code's reply ("●")
// and its tool's or notice's output ("⎿"). Only they end an echo.
const limitAnswerGlyphs = "●⎿"

// limitWrapRows is how many rows under a notice's first it may wrap onto:
// Codex's longest, its links and its time, on a phone's narrow screen.
const limitWrapRows = 4

// harnessLimitIn reports whether the bottom of a screen has the CLI here
// saying it is at its limit, and when that resets (zero when it does not
// say, or the time cannot be read). prog is the program running here; ""
// when not known, and then every CLI's notices count.
func harnessLimitIn(tail, prog string, now time.Time) (bool, time.Time) {
	n, at, _ := harnessLimitNoticesIn(tail, prog, now)
	return n > 0, at
}

// harnessLimitNoticesIn is harnessLimitIn with how many notices the screen
// shows — a notice from before the agent worked again stays in its
// conversation, and only one more than those is news — and whether the
// lowest of them answers the last message the screen shows: it is below
// that message's echo. Counted, a screen full of notices reads the same
// after one more (the rows read are few: the oldest goes as the newest
// comes); placed, the newest answers the last message (review). False with
// no echo on the screen: a harness that echoes
// none, or one scrolled away.
func harnessLimitNoticesIn(tail, prog string, now time.Time) (int, time.Time, bool) {
	if !isAgentProgram(prog) {
		prog = ""
	}
	count, at := 0, time.Time{}
	lowestNotice, lowestEcho, noticeRows := -1, -1, 0
	rows := strings.Split(tail, "\n")
	for i, row := range rows {
		flat := strings.ToLower(strings.Join(strings.Fields(row), ""))
		if flat == "" {
			continue
		}
		lead := strings.IndexFunc(flat, func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) })
		if lead < 0 {
			continue
		}
		if strings.ContainsAny(flat[:lead], limitPromptGlyphs) {
			// A person's text, at the prompt or echoed. An echo is a message
			// the agent was sent; its input box (the rule right under it, or
			// nothing: the cursor's row) is not one.
			if limitEchoRow(rows[i+1:]) {
				lowestEcho = i
			}
			continue
		}
		if strings.ContainsAny(flat[:lead], limitQuoteGlyphs) {
			continue // quoted
		}
		if strings.ContainsAny(flat[:lead], limitProseGlyphs) {
			continue // a list, a heading, a table
		}
		core := flat[lead:]
		// A long notice wraps (on a narrow screen, onto several rows), and
		// Gemini CLI says the time on a row of its own: the rows below it are
		// read with it.
		below := rows[i+1 : min(i+2+limitWrapRows, len(rows))]
		var more []string
		for _, next := range below[:min(limitWrapRows, len(below))] {
			more = append(more, strings.ToLower(strings.Join(strings.Fields(next), "")))
		}
		ok, used := limitRowIn(flat[:lead], core, row, more, rows[i+1:], prog)
		if !ok {
			continue
		}
		// The lowest notice is the latest: its reset time is the one that
		// stands.
		count++
		lowestNotice, noticeRows = i, used
		// When it resets: from the rows it took, or the two under it (where
		// Gemini CLI says it), never further.
		said := core + strings.Join(more[:max(used, min(2, len(more)))], "")
		if t := parseLimitReset(said, now); !t.IsZero() {
			at = t
		}
	}
	if prog == "" || prog == "claude" {
		if said := claudeFooterLimit(rows); said != "" {
			// Waiting to go on by itself, it is at its limit: said under its
			// input box, the latest thing on the screen.
			count++
			if t := parseLimitReset(said, now); !t.IsZero() {
				at = t
			}
			return count, at, true
		}
	}
	if lowestNotice < 0 {
		return count, at, false
	}
	if lowestEcho >= 0 {
		return count, at, lowestNotice > lowestEcho
	}
	// No echo in view: a long message's first row (its only one at the
	// prompt's mark) scrolled above the rows read (review). Then the notice
	// answers it if it is the last thing above the
	// input box — not if the agent's work followed it.
	return count, at, limitLastAboveBox(rows[lowestNotice+1+noticeRows:])
}

// claudeWaitFooter is what Claude Code's footer says while it waits to go on
// by itself after a usage limit, flattened (2.1.312, its "limit-status" and
// "limit-next" rows): "Usage limit reached · limit resets 3pm", "Continuing
// automatically at 3pm · Esc to cancel".
var claudeWaitFooter = regexp.MustCompile(`^(?:usagelimitreached(?:·limitresets` + limitWhen + `)?` +
	`|continuing(?:automatically(?:at` + limitWhen + `|whenitresets)|shortly)·esctocancel)$`)

// limitFooterGap is the run of spaces between a status row's parts.
var limitFooterGap = regexp.MustCompile(`\s{2,}`)

// claudeFooterLimit is what Claude Code's footer says of a usage limit it
// waits out, flattened, or "": read only under its input box, where an
// answer never is, each row whole or as its status row's right-hand part
// ("⏵⏵ auto mode on (shift+tab to cycle)    Usage limit reached · …").
// The footer is the screen's last rows: under it, a program drawn over
// Claude Code (an editor's "~" rows, a dialog's border, a file's name in
// quotes) says the box above is not Claude's live one (review).
func claudeFooterLimit(rows []string) string {
	said := ""
	for _, r := range rows[claudeBoxEnd(rows)+1:] {
		t := strings.TrimSpace(r)
		if claudeFooterNeverRow(r, t) {
			return ""
		}
		parts := limitFooterGap.Split(t, -1)
		if end := strings.ToLower(strings.Join(strings.Fields(parts[len(parts)-1]), "")); claudeWaitFooter.MatchString(end) {
			said += end
		}
	}
	return said
}

// claudeFooterNever open rows Claude Code's footer never draws: an editor's
// empty lines, a box's border, a quote.
const claudeFooterNever = "~│╭╰┃┌└" + limitQuoteGlyphs

// claudeFooterNeverRow says whether a row (r as drawn, t trimmed) is one
// Claude Code's footer never draws: one claudeFooterNever opens, or, at the
// row's start, an editor's mode ("-- INSERT --") or a pager's prompt (":",
// "(END)"), padded or not, the mode perhaps with more after it ("--
// INSERT --recording @q"). Claude Code indents its footer, its own vim mode
// ("-- INSERT --") too (review).
func claudeFooterNeverRow(r, t string) bool {
	return strings.ContainsAny(firstRune(t), claudeFooterNever) ||
		!strings.HasPrefix(r, " ") && (t == ":" || t == "(END)" || editorModeRe.MatchString(t))
}

// editorModeRe is an editor's mode at its row's start: "-- INSERT --".
var editorModeRe = regexp.MustCompile(`^-- [A-Z]+(?: [A-Z]+)* --`)

// claudeBoxEnd is the row of the rule that closes Claude Code's lowest input
// box (a rule, its prompt "❯" at the row's start, perhaps a draft's rows, a
// rule), or the screen's last row when it shows none: nothing is under it.
// Its end is the first row under the prompt that is the same rule as the
// one above it, as Claude Code draws both: a draft's rules are indented (a
// pasted screen), and a status line's own bar comes after it. A screen
// redrawn at a new width may leave the two unlike: then the first rule
// under it. Only the lowest box counts: one above it is an older frame
// (review).
func claudeBoxEnd(rows []string) int {
	for i := len(rows) - 1; i > 0; i-- {
		top := strings.TrimRight(rows[i-1], " ")
		if !strings.HasPrefix(rows[i], "❯") || !attnIsRule(top) {
			continue
		}
		end := -1
		for j := i + 1; j < len(rows); j++ {
			if strings.TrimRight(rows[j], " ") == top {
				return j
			}
			if end < 0 && attnIsRule(rows[j]) {
				end = j
			}
		}
		if end >= 0 {
			return end
		}
		break
	}
	return len(rows) - 1
}

// limitLastAboveBox says whether rows, those under a notice, are its input
// box and what is drawn with it: a rule, then its prompt row — with only a
// turn's time ("✻ Churned for 0s") between.
func limitLastAboveBox(rows []string) bool {
	for i, r := range rows {
		t := strings.TrimSpace(r)
		switch {
		case t == "":
			continue
		case attnIsRule(r):
			for _, b := range rows[i+1:] {
				if b := strings.TrimSpace(b); b != "" {
					return strings.ContainsAny(firstRune(b), limitPromptGlyphs)
				}
			}
			return false
		case limitUnderNotice(t):
			continue
		default:
			return false
		}
	}
	return false
}

func firstRune(s string) string {
	for _, r := range s {
		return string(r)
	}
	return ""
}

// limitUnderNotice says whether t, a row trimmed, is one drawn under a
// notice before its prompt: a turn's time line ("✻ Churned for 0s"), or the
// notice's own hint ("/upgrade to increase your usage limit.", "Run
// /usage-credits to ask your admin for more"), named and whole: a sentence
// that starts with a command ("/usage shows how much is left") is not one
// (review).
func limitUnderNotice(t string) bool {
	return strings.HasPrefix(t, "✻") || limitHintRe.MatchString(strings.ToLower(strings.Join(strings.Fields(t), "")))
}

// limitHintRe is a notice's hint on a row of its own, flattened: the
// commands limitCommand names, with only their own words, or "/upgrade to
// increase your usage limit."
var limitHintRe = regexp.MustCompile(`^(?:run)?(?:/upgradetoincreaseyourusagelimit|` +
	strings.TrimSuffix(strings.TrimPrefix(limitCommand, `(?:[·.](?:run)?`), `)?`) + `)\.?$`)

// limitAnswerEnds says whether rows, those under a notice, end its answer:
// up to the prompt (its mark at the row's start, not an answer's indented
// "> " quote), or the screen's end, only rows with no words (a box's rule),
// a turn's time line or the notice's hint. A row of words, the answer going
// on, says not.
func limitAnswerEnds(rows []string) bool {
	for _, r := range rows {
		t := strings.TrimSpace(r)
		switch {
		case strings.ContainsAny(firstRune(r), limitPromptGlyphs):
			return true
		case limitUnderNotice(t), !strings.ContainsFunc(t, func(c rune) bool { return unicode.IsLetter(c) || unicode.IsDigit(c) }):
			continue
		default:
			return false
		}
	}
	return true
}

// limitEchoRow says whether a row at a prompt's mark, with below rows,
// echoes a message sent — not the input box. A message's rows go on as
// plain text either way; under an echo comes what answered it, a row
// opening with its glyph ("⎿", "●"), and under the box its rule, or
// nothing (the cursor's row). A person's draft of many rows in the box is
// not an echo (review).
func limitEchoRow(below []string) bool {
	for _, r := range below {
		t := strings.TrimSpace(r)
		if t == "" {
			continue
		}
		if attnIsRule(r) {
			return false
		}
		if strings.ContainsAny(firstRune(t), limitAnswerGlyphs) {
			return true
		}
		// The message's own rows: its words, a list's "- ", a code line's
		// backtick — a draft in the box has them too (review).
	}
	return false
}

// limitHintStarts open a row a CLI draws under its notice, in lower case:
// a key to press, a command to run (review).
var limitHintStarts = []string{"esc ", "ctrl", "run /", "press ", "enter to", "tab to", "shift+"}

// limitJoinMax is the longest a notice reads, flattened, with the rows it
// wraps onto (Codex's, with its links, is under 250): rows are joined only
// up to it, so a screen of long rows is read as fast as one of short ones.
const limitJoinMax = 320

// limitRowIn reports whether a row (core: from its first letter or digit;
// lead: the marks before it; row: as drawn) is a limit notice of prog; of
// any CLI when prog is "". more are the rows below it, flattened, that a
// whole notice may wrap onto; used, how many it did. rest are the rows
// under it as drawn.
//
// A whole notice is a CLI's own row: it starts with a capital or a digit (a
// row that starts in lower case goes on a sentence from the row above), and
// the row after it does not go on in lower case — that is a sentence
// wrapping on, unless it is a hint the CLI draws under its notice ("esc to
// interrupt", "run /usage-credits …").
func limitRowIn(lead, core, row string, more, rest []string, prog string) (bool, int) {
	below := rest[:min(limitWrapRows+1, len(rest))]
	first := strings.IndexFunc(row, func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) })
	ownRow := first >= 0 && !unicode.IsLower([]rune(row[first:])[0])
	drawn := strings.Join(strings.Fields(row), " ")
	goesOn := func(used int) bool {
		if used >= len(below) {
			return false
		}
		t := strings.TrimSpace(below[used])
		r := []rune(t)
		if len(r) == 0 || !unicode.IsLower(r[0]) {
			return false
		}
		for _, h := range limitHintStarts {
			if strings.HasPrefix(t, h) {
				return false
			}
		}
		return true
	}
	joins := []string{core}
	for _, m := range more {
		j := joins[len(joins)-1] + m
		if len(j) > limitJoinMax {
			break
		}
		joins = append(joins, j)
	}
	match := func(ns []limitNotice) (bool, int) {
		for _, n := range ns {
			if n.glyph != "" && !strings.ContainsAny(lead, n.glyph) {
				continue
			}
			if !n.whole {
				if n.re.MatchString(core) {
					return true, 0
				}
				continue
			}
			own := ownRow || n.modelLead && first >= 0 && modelLeads(row[first:])
			if !own || (n.cased != "" && !strings.Contains(drawn, n.cased)) {
				continue
			}
			for used, j := range joins {
				if n.re.MatchString(j) && !goesOn(used) && (!n.last || limitAnswerEnds(rest[min(used, len(rest)):])) {
					return true, used
				}
			}
		}
		return false, 0
	}
	if prog != "" {
		return match(harnessLimitNotices[prog])
	}
	for _, ns := range harnessLimitNotices {
		if ok, used := match(ns); ok {
			return true, used
		}
	}
	return false, 0
}

// ---- when it resets ---------------------------------------------------------

// A notice says when the limit resets in one of three ways, each read from
// the flattened row (no spaces: "resets1:30pm(america/los_angeles)"):
//   - a clock time, perhaps a date and a zone: "resets 1:30pm",
//     "resets at 2pm (Europe/London)", "try again at 3:05 PM",
//     "resets Oct 20, 1pm";
//   - a wait: "try again in 2h 5m", "in 4 days 2 hours 5 minutes";
//   - an instant: "2026-10-20T14:00:00Z", "2026-10-20 14:00 UTC".
var (
	limitISORe   = regexp.MustCompile(`(\d{4}-\d{2}-\d{2})[t ]?(\d{1,2}:\d{2}(?::\d{2})?)(z|utc|[+-]\d{2}:?\d{2})?`)
	limitWaitRe  = regexp.MustCompile(`(?:tryagain|retry|resets?|available|automatically)(?:in|after)((?:\d+(?:days?|d|hours?|hrs?|h|minutes?|mins?|m|seconds?|secs?|s),?(?:and)?)+)`)
	limitWaitPt  = regexp.MustCompile(`(\d+)(days?|d|hours?|hrs?|h|minutes?|mins?|m|seconds?|secs?|s)`)
	limitClockRe = regexp.MustCompile(`(?:resets?|resetsat|resetat|tryagainat|availableat|at)(?:on)?(?:(jan|feb|mar|apr|may|jun|jul|aug|sep|oct|nov|dec)[a-z]*(\d{1,2})(?:st|nd|rd|th)?,?(\d{4})?(?:at)?)?(\d{1,2})(?::(\d{2}))?(am|pm)?(?:\(([a-z0-9_+\-/]+)\)|(utc|gmt|[pmce][sd]t|bst|cet|cest))?`)
)

// limitWaitMax is the longest wait a notice is taken to give: a plan's
// longest window is a month; "in 99999 days" is no reset time.
const limitWaitMax = 31 * 24 * time.Hour

// parseLimitReset reads when a limit resets from a flattened notice row; zero
// when it does not say, or says it in a way not read here.
func parseLimitReset(flat string, now time.Time) time.Time {
	if m := limitISORe.FindStringSubmatch(flat); m != nil {
		if t, ok := limitISO(m, now); ok {
			return t
		}
	}
	if m := limitWaitRe.FindStringSubmatch(flat); m != nil {
		var d time.Duration
		for _, p := range limitWaitPt.FindAllStringSubmatch(m[1], -1) {
			n, _ := strconv.Atoi(p[1])
			switch p[2][0] {
			case 'd':
				d += time.Duration(n) * 24 * time.Hour
			case 'h':
				d += time.Duration(n) * time.Hour
			case 'm':
				d += time.Duration(n) * time.Minute
			case 's':
				d += time.Duration(n) * time.Second
			}
		}
		if d > 0 && d <= limitWaitMax {
			return now.Add(d).Truncate(time.Second)
		}
	}
	for _, m := range limitClockRe.FindAllStringSubmatch(flat, -1) {
		if t, ok := limitClock(m, now); ok {
			return t
		}
	}
	return time.Time{}
}

func limitISO(m []string, now time.Time) (time.Time, bool) {
	clock := m[2]
	if strings.Count(clock, ":") == 1 {
		clock += ":00"
	}
	loc := now.Location()
	switch z := m[3]; {
	case z == "z" || z == "utc":
		loc = time.UTC
	case z != "":
		z = strings.Replace(z, ":", "", 1)
		sign := 1
		if z[0] == '-' {
			sign = -1
		}
		h, _ := strconv.Atoi(z[1:3])
		mm, _ := strconv.Atoi(z[3:5])
		loc = time.FixedZone("", sign*(h*3600+mm*60))
	}
	t, err := time.ParseInLocation("2006-01-02 15:04:05", m[1]+" "+clock, loc)
	return t, err == nil
}

var limitMonths = map[string]time.Month{
	"jan": time.January, "feb": time.February, "mar": time.March, "apr": time.April,
	"may": time.May, "jun": time.June, "jul": time.July, "aug": time.August,
	"sep": time.September, "oct": time.October, "nov": time.November, "dec": time.December,
}

// limitClock reads "1:30pm", "13:30", "oct20,1pm(europe/london)", "oct21st,
// 20263:05pm", "3:05pmpdt": a clock time with no date is its next one from
// now (for a notice, what noteHarnessLimit reads it against), in the zone
// named, else the machine's own.
//
// m: month, day, year, hour, minute, am/pm, zone (named), zone (short).
func limitClock(m []string, now time.Time) (time.Time, bool) {
	h, err := strconv.Atoi(m[4])
	if err != nil {
		return time.Time{}, false
	}
	mins := 0
	if m[5] != "" {
		mins, _ = strconv.Atoi(m[5])
	}
	switch m[6] {
	case "am":
		if h == 12 {
			h = 0
		}
	case "pm":
		if h != 12 {
			h += 12
		}
	default:
		// No am/pm: only a 24-hour time with minutes ("13:30") says a time;
		// a bare number ("at 2") is not one.
		if m[5] == "" {
			return time.Time{}, false
		}
	}
	if h > 23 || mins > 59 {
		return time.Time{}, false
	}
	loc := now.Location()
	if m[7] != "" {
		l := limitZone(m[7])
		if l == nil {
			return time.Time{}, false // a zone not known here: no time, not the machine's
		}
		loc = l
	} else if m[8] != "" {
		loc = limitShortZone(m[8], now)
	}
	local := now.In(loc)
	y, mo, d := local.Date()
	dated := false
	if mon, ok := limitMonths[m[1]]; ok && m[2] != "" {
		day, _ := strconv.Atoi(m[2])
		mo, d, dated = mon, day, true
		if m[3] != "" {
			y, _ = strconv.Atoi(m[3])
		}
	}
	t := time.Date(y, mo, d, h, mins, 0, 0, loc)
	if t.Day() != d {
		return time.Time{}, false // "oct32", "feb30": no such day
	}
	if dated {
		if m[3] == "" && t.Before(local.Add(-24*time.Hour)) {
			t = t.AddDate(1, 0, 0) // "Jan 2" read on Dec 31
		}
		return t, true
	}
	if !t.After(local) {
		t = t.AddDate(0, 0, 1) // already past today: tomorrow's
	}
	return t, true
}

// limitShortZones are the short zone names a notice may give (Gemini CLI's
// "3:05 PM PDT"), as offsets from UTC.
var limitShortZones = map[string]int{
	"utc": 0, "gmt": 0, "bst": 1, "cet": 1, "cest": 2,
	"est": -5, "edt": -4, "cst": -6, "cdt": -5, "mst": -7, "mdt": -6, "pst": -8, "pdt": -7,
}

// limitShortZone is a short zone name as a location: the machine's own when
// its zone goes by that name now (so its rules apply), else the fixed offset.
func limitShortZone(name string, now time.Time) *time.Location {
	if n, _ := now.Zone(); strings.EqualFold(n, name) {
		return now.Location()
	}
	if off, ok := limitShortZones[name]; ok {
		if off == 0 {
			return time.UTC
		}
		return time.FixedZone(strings.ToUpper(name), off*3600)
	}
	return now.Location()
}

// limitZone is the zone a notice names, as the row's lowercase spells it:
// "america/los_angeles" is America/Los_Angeles, "utc" is UTC.
func limitZone(name string) *time.Location {
	if name == "utc" || name == "gmt" || name == "etc/utc" {
		return time.UTC
	}
	parts := strings.Split(name, "/")
	for i, p := range parts {
		words := strings.Split(p, "_")
		for j, w := range words {
			if w != "" {
				words[j] = strings.ToUpper(w[:1]) + w[1:]
			}
		}
		parts[i] = strings.Join(words, "_")
	}
	if l, err := time.LoadLocation(strings.Join(parts, "/")); err == nil {
		return l
	}
	return nil
}

// ---- the state, debounced ---------------------------------------------------

// harnessLimitState is the debounced limit state; guarded by Agent.metaMu.
// Like the login state: seen in harnessDownAfter reads to count, and cleared
// once the agent is seen at work, a turn begun since is answered with no
// notice of its own, or the notice has been gone harnessUpAfter reads or
// harnessUpFor.
type harnessLimitState struct {
	limited        bool
	resetsAt       time.Time
	since          time.Time // when it was found limited
	seen, gone     int
	seenAt, goneAt time.Time
	// stale is how many notices on the screen are from before the agent
	// worked again: they stay in its conversation, and are not news.
	stale int
	// readFrom is what a notice's reset time is read against (a time with
	// no date is its next one after it): when the turn it answered ended,
	// kept for the lapse. Read against each read's now, "resets 3pm" still
	// on the screen at 15:00:20 was tomorrow's 3pm, and the clock set for
	// it moved a day (review).
	readFrom time.Time
	// last is how many notices the last read showed.
	last int
	// staleAnswered: the lowest notice answered the last message when the
	// limit was last over, and has since — no message after it: not news.
	staleAnswered bool
	// turnSeen is the last turn begun the lapse has heard of; turn, one
	// begun while limited and not yet answered, with turnN the notices the
	// screen had before it, and turnEnd when its hook said it ended.
	turnSeen, turn, turnEnd time.Time
	turnN                   int
	turnFailure             string // what its hook said ended that turn ("" for a Stop)
}

// limitRefusals are the errors a harness's hook names for a turn its limit
// (or its login) turned away: Claude Code's StopFailure "rate_limit", its
// usage credits ("billing_error") and a login gone ("authentication_failed").
// Its others ("overloaded", "server_error", "unknown") are not the limit's
// word: a session whose limit has lifted is not held by them (review).
var limitRefusals = map[string]bool{"rate_limit": true, "billing_error": true, "authentication_failed": true}

// limitReadFrom is what a notice first seen now is read against: when the
// turn it answered ended, as its hook said — kept on disk, so a reminal
// started again after the reset reads the same time it did — else now (a
// notice drawn while a turn runs, a CLI with no hook). An end over a day old
// is no notice's on the screen now: a time with no date is within a day.
func limitReadFrom(turnAt, endAt, now time.Time) time.Time {
	if endAt.IsZero() || endAt.Before(turnAt) || endAt.After(now) || now.Sub(endAt) > 24*time.Hour {
		return now
	}
	return endAt
}

// noteHarnessLimit is called with each attention tick's screen tail (the
// rows harnessTail kept), the program here, whether its hook shows it at
// work (harnessAtWork), when its hook last said a turn began (zero if it
// has not) and when it said that turn ended (zero while it runs, or not
// said). It reports whether the limit state changed.
//
// A turn begun after the limit was found is its test: if the limit still
// holds, the harness answers it with a notice of its own — one more than
// the screen had, the old ones staying in the conversation above — and the
// lapse holds: a refused "continue" is the same lapse, never a new one
// (review). Answered with none, by its end and harnessUpFor after, or seen
// at work: over.
func (a *Agent) noteHarnessLimit(tail, prog string, atWork bool, turnAt, endAt time.Time, failure string) bool {
	now := harnessNow()
	a.metaMu.Lock()
	defer a.metaMu.Unlock()
	l := &a.limit
	from := l.readFrom
	if from.IsZero() {
		from = limitReadFrom(turnAt, endAt, now)
	}
	n, at, answered := harnessLimitNoticesIn(tail, prog, from)
	before := l.last
	l.last = n
	if n < l.stale {
		l.stale = n // scrolled away
	}
	if !answered {
		l.staleAnswered = false // a message since: the next notice under it is news
	}
	over := func() bool {
		*l = harnessLimitState{stale: n, last: n, staleAnswered: answered}
		return true
	}
	if l.limited {
		if atWork {
			return over() // at work again: the limit is over, whatever its notice said
		}
		if turnAt.After(l.turnSeen) {
			l.turnSeen, l.turn, l.turnN, l.turnEnd, l.turnFailure = turnAt, turnAt, before, time.Time{}, ""
		}
		if !l.turn.IsZero() {
			if endAt.After(l.turn) {
				l.turnEnd, l.turnFailure = endAt, failure
			}
			ended := !l.turnEnd.IsZero()
			switch {
			case ended && limitRefusals[l.turnFailure]:
				// Its harness said the limit, or the login, ended it (Claude
				// Code's StopFailure: "rate_limit"): turned away, whatever
				// the screen shows — the lapse holds. Another error (the
				// service overloaded, the network) says nothing of the
				// limit: the screen decides, as for a plain end.
				l.turn, l.turnEnd, l.turnFailure = time.Time{}, time.Time{}, ""
			case n > l.turnN:
				l.turn, l.turnEnd = time.Time{}, time.Time{} // turned away: the lapse holds
			case ended && now.Sub(l.turnEnd) >= harnessUpFor:
				// Ended with no error said: turned away if a notice answers
				// the last message still — a screen full of notices has one
				// more no longer counted — else answered with none of its
				// own: over. A plain end is no proof by itself: Claude Code
				// before 2.1.78 (no StopFailure) ends a turn its limit
				// turned away with Stop, as does a harness with no error
				// hook (tried in Docker).
				if !answered {
					return over()
				}
				l.turn, l.turnEnd = time.Time{}, time.Time{}
			}
		}
	}
	hit := !atWork && (n > l.stale || answered && !l.staleAnswered)
	changed := false
	switch {
	case hit:
		if l.seen == 0 {
			l.seenAt = now
		}
		if l.readFrom.IsZero() {
			l.readFrom = from
		}
		l.gone = 0
		if l.seen++; !l.limited && l.seen >= harnessDownAfter {
			l.limited, l.since, l.turnSeen, changed = true, now, turnAt, true
		}
		if l.limited && !at.IsZero() && !at.Equal(l.resetsAt) {
			l.resetsAt, changed = at, true
		}
	default:
		if l.gone == 0 {
			l.goneAt = now
		}
		l.seen = 0
		if !l.limited {
			l.readFrom = time.Time{} // a glimpse: the next is read afresh
		}
		if l.gone++; l.limited && (l.gone >= harnessUpAfter || now.Sub(l.goneAt) >= harnessUpFor) {
			changed = over()
		}
	}
	return changed
}

// harnessLimitedNow reports whether the agent here is at its limit, and
// when that resets (zero when not said).
func (a *Agent) harnessLimitedNow() (bool, time.Time) {
	a.metaMu.Lock()
	defer a.metaMu.Unlock()
	return a.limit.limited, a.limit.resetsAt
}

// harnessLimitReadAt is when the screen should be read again for the limit
// state to settle (as harnessReadAt for the login); zero when nothing waits.
func (a *Agent) harnessLimitReadAt() time.Time {
	a.metaMu.Lock()
	defer a.metaMu.Unlock()
	l := &a.limit
	switch {
	case !l.limited && l.seen > 0:
		return l.seenAt.Add(attnInterval)
	case l.limited && l.gone > 0:
		return l.goneAt.Add(harnessUpFor)
	case l.limited && !l.turnEnd.IsZero():
		return l.turnEnd.Add(harnessUpFor) // a turn answered: over unless its notice comes
	}
	return time.Time{}
}
