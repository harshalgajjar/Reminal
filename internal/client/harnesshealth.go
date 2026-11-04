// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

import (
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"

	"reminal/internal/session"
)

// Whether the agent in this session can work at all. A harness whose login
// has expired still draws its prompt, reads the line typed into it, and ends
// the turn at once with "Login expired · Please run /login": to everything
// that watches turns, the message was delivered and handled. It was not —
// and every later one would go the same way, until a person logs it in.
//
// So the bottom of the screen, where harnesses put their status line and
// such errors, is read on every attention tick, and while it says the agent
// is logged out the session reports that instead of an attention state: a
// session that looks idle is one you leave alone, and this one needs you.

// harnessLoggedOut is the state a session reports while its agent cannot
// work for want of a login. It is not one of the attention states — those say
// what the agent is doing, and this says it can do nothing.
const harnessLoggedOut = "logged-out"

// What agent CLIs print when they cannot run for want of a login. Matched
// with all whitespace removed: Claude Code draws the spaces between words as
// cursor movements, so its screen reads "●Loginexpired·Pleaserun/login" and
// "Notloggedin·Run/login" — and must match whether or not spaces render.
//
// harnessLoginNotice is Claude Code's shape: what went wrong, then the
// command to run. The command in the same breath is what tells it from an
// agent's own prose about logins ("…was not logged in, so the redirect to
// /login looped" does not match).
// harnessLoginRefused is Claude Code's other shape, for a login it was
// given and the service turned away: the command first, then the error
// ("● Please run /login · API Error: 401 OAuth access token is invalid."),
// on a line of its own — read from that line's start, so a sentence that
// quotes it is not it. Missed, the agent sat at its prompt and read as
// waiting for an approval.
var harnessLoginRefused = regexp.MustCompile(`^(please)?run/login[·:.,\-]*apierror:40[13]([^0-9]|$)`)

var harnessLoginNotice = regexp.MustCompile(`(loginexpired|loginreacheditstimelimit|notloggedin|invalidapikey|oauthtoken(has)?(expired|revoked))[·:.,\-]*(please)?run/login`)

// loginRow is one way a CLI says it cannot run for want of a login, as its
// row reads with all whitespace removed, in lower case.
type loginRow struct {
	re *regexp.Regexp
	// whole: the CLI's own row, from its first letter or digit to its end,
	// read with the rows it wraps onto (loginRows). A row that starts with
	// its words and goes on in words of its own is a sentence, not it ("Sign
	// in with ChatGPT auth; API key auth cannot load remote plugin
	// catalogs"); nor is one that a quote, a list or a heading opens. Not
	// whole: matched on the row as it is (loginOpen).
	whole bool
	// bottom: only on the last few rows (harnessLineRows), the CLI's own
	// status: words plain enough for an agent to end a line with.
	bottom bool
	// marks: words an agent could write as a sentence of its own ("Your
	// session has expired. Please reauthenticate."), so the CLI's only on a
	// row led by one of these marks — the one it draws its notices with —
	// or on the last few rows.
	marks string
	// then: a row that is the CLI's only with another of its own below it,
	// within the next few: a menu's first item, with the next ("1. Sign in
	// with ChatGPT", then "2. Sign in with Device Code") — one numbered item
	// alone is an agent's list.
	then *regexp.Regexp
}

// loginRowEnd is what a whole row may end on after its words: a full stop,
// an ellipsis, "!" — not ":", which opens what an agent goes on to write.
// A row whose CLI ends it on ":", "?" or ">" says so in its pattern.
const loginRowEnd = `(?:\.|…|!)*$`

// loginRows are rows read whole: each's words, then only loginRowEnd.
func loginRows(res ...string) []loginRow {
	out := make([]loginRow, len(res))
	for i, r := range res {
		out[i] = loginRow{re: regexp.MustCompile("^(?:" + r + ")" + loginRowEnd), whole: true}
	}
	return out
}

// loginBottom are loginRows read only on the bottom rows.
func loginBottom(res ...string) []loginRow {
	out := loginRows(res...)
	for i := range out {
		out[i].bottom = true
	}
	return out
}

// loginMarked are loginRows read only on a row led by one of marks, or on
// the bottom rows.
func loginMarked(marks string, res ...string) []loginRow {
	out := loginRows(res...)
	for i := range out {
		out[i].marks = marks
	}
	return out
}

// loginThen is a loginRow read only with a row matching then (whole) among
// the few below it.
func loginThen(re, then string) []loginRow {
	out := loginRows(re)
	out[0].then = regexp.MustCompile("^(?:" + then + ")" + loginRowEnd)
	return out
}

// loginOpen is a pattern matched on the row as it is, its own anchors its own.
func loginOpen(re string) []loginRow {
	return []loginRow{{re: regexp.MustCompile(re)}}
}

func loginAll(rs ...[]loginRow) []loginRow {
	var out []loginRow
	for _, r := range rs {
		out = append(out, r...)
	}
	return out
}

// harnessLoginScreens are, by CLI, the sign-in screens it starts on when it
// has no login (a menu of ways to sign in, not an error), and the notices it
// turns a message away with when its login has lapsed. A message typed into a
// sign-in screen picks a menu item or answers its question: Amp's "Would you
// like to log in? [(y)es, (n)o]" took the first letter of a welcome as a no,
// and the login was gone. Each counts only when its own CLI is the program
// here: a Claude Code session whose tool printed Codex's sign-in menu (a check
// of Codex's login, 2026-10-14) is not logged out, and was read as one for as
// long as that output was on screen.
var harnessLoginScreens = map[string][]loginRow{
	// Antigravity CLI: its sign-in screen's heading ("Welcome to the
	// Antigravity CLI. You are currently not signed in."), and its status.
	// 1.5: a sign-in that lapsed mid-session, and its sign-in steps: the
	// link to open, then the code to paste (a message typed there is taken
	// as the code).
	"agy": loginAll(loginRows(
		`(?:welcometotheantigravitycli\.|welcomeback!|noauthenticationmethodsavailable\.+)youarecurrentlynotsignedin`,
		`authenticationexpired\.pleaselogbackin`,
		`openthisurlinyourlocalbrowsertosignin:?`,
		`openthislinkinabrowsertologinwithgoogle:?`,
	), loginBottom(
		`signinrequired`,
		`thenpastetheauthorizationcode,orthefullurlofthepageyoulandon,andpressenter:?`,
	)),
	// Gemini CLI: no way to sign in chosen yet; its Google sign-in waiting
	// for the code; its API key dialog; no key at all; and a key it cannot
	// use. That last is the service's own words, inside the JSON of the
	// error it prints for the turn ("✕ [API Error: {…\"message\": \"API key
	// not valid. Please pass a valid API key.\" …"), wrapped at the
	// terminal's width wherever the width falls: by that JSON's key and the
	// words' start, not a row of its own.
	"gemini": loginAll(loginOpen(`message[\\"]*:[\\"]*apikeynotvalid\.please`), loginRows(
		`howwouldyouliketoauthenticateforthisproject\?`,
		`noauthenticationmethodselected`,
		`pleasevisitthefollowingurltoauthorizetheapplication:?`,
		`entergeminiapikey`,
		`geminiapikeyismissingornotconfigured`,
	), loginBottom(
		`entertheauthorizationcode:?`,
	)),
	// Amp: its question before it signs in, and what it says of a login it
	// has not got or that lapsed, each with the command it names.
	"amp": loginAll(loginRows(
		`wouldyouliketologintoamp\?\[\(y\)es,\(n\)o\]:?`,
		`noapikeyfound\.(?:startingloginflow|run.?amplogin.?first)`,
		`authenticationfailed\.run.?amplogin.?andtryagain`,
		`apikeyrequired(?:for(?:--no-tui|headlessmode))?\.pleaserun.?amplogin.?first`,
		`sessionexpiredandcouldnotberefreshed\.run.?amplogin.?toauthenticate`,
		`accesstokenexpired\.run.?amplogin.?torefresh`,
		`invalidormissingapikey\.run.?amplogin.?toauthenticate`,
	), loginBottom(
		`yoursessionhasexpired\.youneedtologinagain`,
	)),
	// Codex: its sign-in menu (its heading, and its first item with the next
	// below it: "› 1. Sign in with ChatGPT", "2. Sign in with Device Code"),
	// signing in through the browser, and a login it can no longer refresh
	// (its errors led by "■").
	"codex": loginAll(loginRows(
		`finishsigninginviayourbrowser`,
		`signinwithchatgpttousecodexaspartofyourpaidplan`,
		`youraccesstokencouldnotberefreshedbecause(?:youhavesinceloggedoutorsignedintoanotheraccount|yourrefreshtoken(?:hasexpired|wasalreadyused|wasrevoked))\.please(?:logoutand)?signinagain`,
		// 0.166: the same said without why, its sign-in that could not be
		// recovered, and a ChatGPT login with no account in it.
		`youraccesstokencouldnotberefreshed\.pleaselogoutandsigninagain`,
		`yourauthenticationsessioncouldnotberefreshedautomatically\.pleaselogoutandsigninagain`,
		"chatgptaccountidnotavailable,pleasere-run`?codexlogin`?",
	), loginThen(`1\.signinwithchatgpt`, `[23]\.(?:signinwithdevicecode|provideyourownapikey)`), loginMarked("■",
		`yoursessionhasexpired\.pleasereauthenticate`,
	)),
	// Qwen Code: its provider menu (0.24: "Connect a Provider", a dialog
	// taller than the rows read, so by its last choice), the steps of
	// setting one up, and a login that lapsed.
	"qwen": loginAll(loginRows(
		`alibabamodelstudio.{0,3}accessmethod`,
		`finishprovidersetup`,
		`connectaprovider`,
		`manuallyconnectalocalserver,proxy,orunsupportedprovider`,
		`noauthtypeisselected\.pleaseconfigureanauthtype\(e\.g\.viasettingsor.?--auth-type.?\)beforerunninginnon-interactivemode`,
		`connectwithanexistingmodelstudioapikey`,
		`noapikeyfound:setopenai_api_keyorsecurity\.auth\.apikey\.?`,
		`qwenoauthcredentialsexpired\.pleaseuse/authtore-authenticatewithqwen-oauth`,
	), loginMarked("✕",
		`refreshtokenexpiredorinvalid\.pleaseuse.?/auth.?tore-authenticate`,
	)),
	// goose 1.55 with no provider: "error: No provider configured. Run
	// 'goose configure' first." (it exits); a provider whose key the service
	// refused; and its Muse Code sign-in lapsed.
	"goose": loginRows(
		`(?:error:)?noproviderconfigured\.run.?gooseconfigure.?first`,
		`(?:error:)?providerisnotconfigured\.run.?gooseconfigure.?tosetitup`,
		`(?:(?:error|ranintothiserror):)?authenticationfailed—checkyourapikey\.run.?gooseconfigure.?toupdateit`,
		`(?:(?:error|ranintothiserror):)?musecodeapikeyismissing\.signinagain`,
		`(?:(?:error|ranintothiserror):)?metasessionexpired\.signintomusecodeagain`,
	),
	// Crush 0.98 with no provider: its first screen ("To start, let's choose
	// a provider and model."), signing in to Hyper, and `crush run`'s error;
	// and a platform's login gone.
	"crush": loginAll(loginRows(
		`tostart,let.?schooseaproviderandmodel`,
		`let.?sauthenticatewithhyper`,
		`(?:error:)?noprovidersconfigured-pleaserun.?crush.?tosetupaproviderinteractively`,
		`pleasesigninto[a-z0-9.\-]+again\.youcanalsorun"crushlogin[a-z0-9.\-]+--force"`,
	), loginBottom(
		`youarenotloggedintoanyplatform`,
	)),
	// OpenCode runs on a free built-in model with no login at all; a
	// provider it was set up for and has no key for says so ("Anthropic API
	// key is missing. Pass it using the 'apiKey' parameter or the
	// ANTHROPIC_API_KEY environment variable."), as does a gateway that
	// turned its token away.
	"opencode": loginRows(
		`[a-z0-9.\-]*apikeyismissing\.passitusingthe'[a-z0-9_]+'parameter(?:orthe[a-z0-9_]+environmentvariable|\.environmentvariables(?:are|is)notsupportedinthisenvironment)`,
		`unauthorized:requestwasblockedbyagatewayorproxy\.yourauthenticationtokenmaybemissingorexpired—tryrunning.?opencodeauthlogin<yourproviderurl>.?tore-authenticate`,
	),
	// Cursor CLI: its first screen with no login ("Press any key to log
	// in..." — a key typed there starts signing in), the sign-in itself, and
	// a login it has not got or can no longer use. A key it was given and
	// cannot use is read apart (harnessKeyRefusals): its words are common
	// enough to come from anything. Its own name in the command is
	// "cursor-agent" or "agent", as it was run.
	"cursor-agent": loginAll(loginRows(
		`pressanykeytologin`,
		`ifyourbrowserdidn.?topen,clickthislinktologin:?`,
		`pressqtoshowaqrcodetologinfromanotherdevice`,
		`(?:error:)?authenticationrequired(?:tousecursoragent)?\.pleaserun.?(?:cursor-)?agentlogin.?(?:toauthenticate|first,orsetcursor_api_keyenvironmentvariable)`,
		`authenticationfailed:yourcursorcredentialsorapikeyareinvalidorexpired\.ifyousetcursor_api_key,checkthatitiscorrect,orrun.?cursor-agentlogin.?tore-authenticate`,
	), loginBottom(
		`yourstoredauthenticationisinvalid\.pleaseloginagain`,
	)),
	// pi with no model to run on (no provider logged in) says so on its own
	// status line, the last on its screen: its context "0" and its model
	// "unknown" ("0.0%/0 (auto)   unknown"). A message sent it is turned away
	// with "Error: No API key found for the selected model." (or for its
	// provider, by name), or, its login lapsed, "Authentication failed for
	// …". Its startup warning ("No models available. Use /login …") sits
	// above its editor, out of the rows read, and stays there after a
	// /login, so it is not used. A login its provider refuses is read apart
	// (harnessKeyRefusals).
	"pi": loginRows(
		`\d+(?:\.\d+)?%/0(?:\([a-z]+\))?unknown`,
		`error:noapikeyfoundfor(?:"[a-z0-9.\-_/]{1,48}"|theselectedmodel|`+piProviders+`)`,
		`(?:error:)?authenticationfailedfor"[a-z0-9.\-_/]{1,48}"\.credentialsmayhaveexpiredornetworkisunavailable\.run'/login[a-z0-9.\-_/]{1,48}'tore-authenticate`,
		`error:yoursessionhasexpired\.pleasereauthenticate`,
	),
	// Claude Code's own login menu (a fresh machine shows it before anything
	// else), and its sign-in step once a way is picked: "Opening browser to
	// sign in…" under a spinner first, then the link. Its own rows, from
	// their start: its conversation can name these words in a sentence. In a
	// narrow terminal the link's heading wraps beside "(c to copy)", so only
	// its start is on its first row ("Browser didn't open? Use the url").
	// Not seen during the spinner, a logged-out Claude Code was read as at
	// work, then done. And the notices it turns a message away with that are
	// not harnessLoginNotice's shape.
	"claude": loginAll(loginRows(
		`selectloginmethod:?`,
		// Its plans, as many as it lists ("Pro, Max, Team, or Enterprise";
		// a later one adds "Free").
		`(?:\d\.)?claudeaccountwithsubscription·(?:,|or|pro|max|team|enterprise|free|plus|business|education)+`,
		`(?:\d\.)?anthropicconsoleaccount·apiusagebilling`,
		`pastecodehereifprompted>?`,
		`openingbrowsertosignin(?:withyourclaudeaccount)?`,
		`browserdidn.?topen\?usetheurl(?:belowtosignin)?(?:\(c(?:to)?(?:copy)?\)?)?`,
	), loginMarked("●⎿",
		`loginexpired·run/logintosigninagain,orre-authenticateyouranthropicprofile`,
		`notsignedintoclaude\.run/loginfirst`,
		`failedtoauthenticate:(?:oauthtokenrevoked\.pleaseloginagainorcontactyouradministrator|oauthsessionexpiredandcouldnotberefreshed)`,
		// 2.1.317: a login at the end of its time, and the Cloud gateway's
		// own sign-in missing or its token lapsed.
		"failedtoauthenticate:thisloginreacheditstimelimit(?:·signinagaintocontinue|andcannotberenewed\\.run`?claudeauthlogin`?tosigninagain,oruse`?claudesetup-token`?andsetclaude_code_oauth_tokenforunattendedjobs)?",
		`notsignedintothecloudgateway—run/login`,
		`cloudgatewaytokenexpired—refreshanthropic_auth_tokenandrestart`,
	)),
}

// piProviders are pi's providers by their ids (pi 1.4.0's own list): what
// its "No API key found for anthropic." names. Any other word there is not
// pi's ("No API key found for staging-server", from a tool).
const piProviders = `amazon-bedrock|ant-ling|anthropic|openai|azure|openai-codex|radius|nvidia|deepseek|google|google-vertex|github-copilot|openrouter|vercel-ai-gateway|xai|groq|cerebras|zai|zai-coding-cn|mistral|minimax|minimax-cn|moonshotai|moonshotai-cn|huggingface|fireworks|together|baseten|opencode|opencode-go|kimi-coding|meta|cloudflare-workers-ai|cloudflare-ai-gateway|qwen-token-plan|qwen-token-plan-cn|qwen-token-plan-individual|xiaomi|xiaomi-token-plan-cn|xiaomi-token-plan-ams|xiaomi-token-plan-sgp`

// cursorKeyRefused is Cursor CLI turning away the API key it was given, its
// own row: "⚠ Warning: The provided API key is invalid.", then where the key
// came from ("The API key was loaded from the CURSOR_API_KEY environment
// variable."), and it exits. A key that was passed and refused, not a login
// missing: the key is what to change.
const cursorKeyRefused = `(?:warning:)?theprovidedapikeyisinvalid`

// harnessKeyRefusals are, by CLI, the rows that say it refused the key it was
// given (not that it has none). Unlike harnessLoginScreens they are taken as
// that CLI's only while it is in front or was just before (noteHarnessHealth):
// "The provided API key is invalid" is also what other tools print, and at a
// plain shell's prompt it says nothing about an agent's login.
var harnessKeyRefusals = map[string][]loginRow{
	"cursor-agent": loginRows(cursorKeyRefused),
	// Claude Code turned away by the login it was given ("● Please run
	// /login · API Error: 401 OAuth access token is invalid"): it has one,
	// and it was refused — not one it never had. A line of its own, from
	// its start, as harnessLoginRefused reads it. And a key from outside it
	// ("Invalid API key · Fix external API key").
	"claude": loginAll(loginOpen(`^[^a-z0-9]*(please)?run/login[·:.,\-]*apierror:40[13]([^0-9]|$)`), loginMarked("●⎿",
		`invalidapikey·fixexternalapikey`,
	)),
	// pi with a login its provider refuses (a token gone stale, a wrong key):
	// it takes the message and answers each with the service's 401 on a row
	// of its own ("Error: 401", then the service's words), at its prompt,
	// ready for more: it read as done. "Error: 401" is also what a script or
	// a curl prints, so only with pi in front (or just before) is it pi's.
	"pi": loginOpen(`^[^a-z0-9]*error:401([^0-9]|$)`),
	// OpenCode with an OpenAI key the service refuses: it takes each message
	// and answers it, in its own box, with the service's words ("┃  Incorrect
	// API key provided: sk-…"), then sits at its prompt again: it read as
	// done. A key it was given and the service refused, from the row's start.
	"opencode": loginOpen(`^[^a-z0-9]*incorrectapikeyprovided`),
	// Codex with an OpenAI key the service refuses: "■ unexpected status 401
	// Unauthorized: Incorrect API key provided: …", from the row's start (the
	// same words in a sentence, or a tool's output at a shell, are not it).
	"codex": loginOpen(`^[^a-z0-9]*(unexpectedstatus40[13][a-z]*:)?incorrectapikeyprovided`),
	// Qwen Code with a key its service refuses, by the plan the key is for,
	// and its provider's credentials refused; in an API error
	// ("API Error: … (Status: 401)").
	"qwen": loginAll(loginRows(
		`(?:apierror:)?invalidapikey\.(?:codingplanapikeysstartwith"sk-sp-"\.pleasecheck|pay-as-you-gokeysstartwith"sk-"or"sk-ws-"\.tokenplankeys\("sk-sp-"\)needthetokenplanentryinstead|tokenplanapikeysstartwith"sk-sp-"\.generateonefromthetokenplanconsole,orusethestandardapikeyentryforapay-as-you-gokey)\.?(?:\(status:40[13]\))?\]?`,
	), loginMarked("✕",
		`(?:apierror:)?modelapiauthenticationfailed\(http401\)\.checktheconfiguredprovidercredentials\.?(?:\(status:40[13]\))?\]?`,
	)),
}

// harnessFrameGlyphs are the sides a dialog draws its rows between: not the
// row's words, so a row that wraps inside one is read as one.
const harnessFrameGlyphs = "│┃║"

// harnessLoginRowMax is the longest a whole row is read at, with the rows it
// wraps onto: the longest notice is a few rows' worth. Past it a row is not
// one, and reading one past it costs every pattern a pass.
const harnessLoginRowMax = 320

// loginLines is a tail's rows, each with all whitespace removed and in lower
// case, the blank ones kept: a blank row ends a paragraph, and a wrapped row
// is never joined across one.
type loginLines struct {
	rows []string
	// bottomFrom is where the last harnessLineRows rows with anything on
	// them start.
	bottomFrom int
}

func harnessFlatRows(tail string) loginLines {
	var l loginLines
	for _, row := range strings.Split(tail, "\n") {
		l.rows = append(l.rows, strings.ToLower(strings.Join(strings.Fields(row), "")))
	}
	l.bottomFrom = len(l.rows)
	for n := 0; l.bottomFrom > 0 && n < harnessLineRows; {
		if l.bottomFrom--; l.rows[l.bottomFrom] != "" {
			n++
		}
	}
	return l
}

// core is row i from its first letter or digit, its dialog's side gone, and
// what leads it; ok false if it is no row of a CLI's (blank, or opened by a
// quote, a list item or a heading: an agent's own text).
func (l loginLines) core(i int) (lead, core string, ok bool) {
	flat := l.rows[i]
	at := strings.IndexFunc(flat, func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) })
	if at < 0 || strings.ContainsAny(flat[:at], limitQuoteGlyphs+limitProseGlyphs) {
		return "", "", false
	}
	return flat[:at], strings.TrimRight(flat[at:], harnessFrameGlyphs), true
}

// harnessLoginRowIn reports whether row i is one of rs. A whole row is read
// alone, then with the one or two rows below it — a long notice wraps at the
// terminal's width — but only where it wraps: never past a blank row, nor
// past a row that ends a sentence (". ", "!", "?"): two rows "Not signed in
// to Claude." and "Run /login first." in an answer are two sentences.
func harnessLoginRowIn(l loginLines, i int, rs []loginRow) bool {
	flat := l.rows[i]
	if flat == "" {
		return false
	}
	bottom := i >= l.bottomFrom
	lead, core, ok := l.core(i)
	for _, r := range rs {
		if r.bottom && !bottom {
			continue
		}
		if !r.whole {
			if r.re.MatchString(flat) {
				return true
			}
			continue
		}
		if !ok || len(core) > harnessLoginRowMax {
			continue
		}
		marked := r.marks != "" && strings.ContainsAny(lead, r.marks)
		if r.marks != "" && !bottom && !marked {
			continue
		}
		// Read only for being on the bottom rows: not if those rows are an
		// agent's answer, or a person's text at the prompt, running on.
		if (r.bottom || (r.marks != "" && !marked)) && l.inAnswer(i) {
			continue
		}
		if r.re.MatchString(core) {
			if r.then == nil || l.followedBy(i, r.then) {
				return true
			}
			continue
		}
		row := core
		for j := i + 1; j < min(i+3, len(l.rows)); j++ {
			next := strings.Trim(l.rows[j], harnessFrameGlyphs)
			if next == "" || strings.ContainsAny(row[len(row)-1:], ".!?") || len(row)+len(next) > harnessLoginRowMax {
				break
			}
			if row += next; r.re.MatchString(row) {
				return r.then == nil || l.followedBy(j, r.then)
			}
		}
	}
	return false
}

// inAnswer reports whether row i is a person's text at the prompt ("❯ …"),
// or in the same paragraph as one or as an answer's first row ("●", "⏺",
// "⎿", Codex's "•", Gemini CLI's "✦"): the rows above it, up to a blank
// row or a row with no words (an input box's rule, an empty prompt).
// "● The staging API returned 401:" then "Authentication required." is an
// agent's answer, not a CLI's status line.
func (l loginLines) inAnswer(i int) bool {
	for j := i; j >= 0; j-- {
		flat := l.rows[j]
		at := strings.IndexFunc(flat, func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) })
		if at < 0 {
			return false
		}
		// The row itself may be the CLI's own notice ("● …"); a person's
		// text it is not.
		// Codex's and Gemini CLI's answers start with their own marks (•, ✦).
		marks := limitAnswerGlyphs + "⏺•✦" + limitPromptGlyphs
		if j == i {
			marks = limitPromptGlyphs
		}
		if strings.ContainsAny(flat[:at], marks) {
			return true
		}
	}
	return false
}

// followedBy reports whether one of the few rows with anything on them
// below row i is then, whole.
func (l loginLines) followedBy(i int, then *regexp.Regexp) bool {
	for j, seen := i+1, 0; j < len(l.rows) && seen < 4; j++ {
		if l.rows[j] == "" {
			continue
		}
		seen++
		if _, core, ok := l.core(j); ok && then.MatchString(core) {
			return true
		}
	}
	return false
}

// harnessKeyRefusedBy reports whether the bottom of a screen has prog saying
// it refused its key (harnessKeyRefusals), read as harnessKeyRefusedIn reads.
func harnessKeyRefusedBy(tail, prog string) bool {
	rs := harnessKeyRefusals[prog]
	if rs == nil {
		return false
	}
	l := harnessFlatRows(tail)
	for i := range l.rows {
		if harnessLoginRowIn(l, i, rs) {
			return true
		}
	}
	return false
}

// harnessKeyRefusedIn reports whether the bottom of a screen has a CLI
// saying it refused its key, and which CLI ("" if none): read with the same
// rows as harnessProblemIn. After the CLI exits on it, its words stay above
// the shell's prompt, the shell in front — so the CLI is the one whose
// words they are, not the program here.
//
// Two CLIs can print the same words (Codex and OpenCode both pass on
// OpenAI's "Incorrect API key provided"): then the one named is the first
// by name, the same every time, not whichever a map's order gave (a test
// read OpenCode's refusal as Codex's half the time). Which of them it was
// is the program's in front, asked by name (harnessKeyRefusedBy).
func harnessKeyRefusedIn(tail string) string {
	l := harnessFlatRows(tail)
	for i := range l.rows {
		for _, prog := range harnessKeyRefusalOrder {
			if harnessLoginRowIn(l, i, harnessKeyRefusals[prog]) {
				return prog
			}
		}
	}
	return ""
}

// harnessKeyRefusalOrder is harnessKeyRefusals' programs by name: the order
// harnessKeyRefusedIn asks them in.
var harnessKeyRefusalOrder = func() []string {
	ps := make([]string, 0, len(harnessKeyRefusals))
	for p := range harnessKeyRefusals {
		ps = append(ps, p)
	}
	sort.Strings(ps)
	return ps
}()

// harnessLoginScreenIn reports whether row i is a sign-in screen's or a
// login notice's row of prog, the program here; of any of them when it is
// not known.
func harnessLoginScreenIn(l loginLines, i int, prog string) bool {
	if prog == "" {
		for _, rs := range harnessLoginScreens {
			if harnessLoginRowIn(l, i, rs) {
				return true
			}
		}
		return false
	}
	return harnessLoginRowIn(l, i, harnessLoginScreens[prog])
}

// harnessLoginLines are other CLIs' notices, the same from any of them: only
// a row of their own on the last few rows — a status line, not a sentence
// in an answer — and only with what they end on ("Authentication required
// for MCP Server: …" is a server's login, not the agent's).
var harnessLoginLines = loginBottom(
	`(?:authenticationrequired|loginrequired|notloggedin|loginexpired)`+loginHowTo,
	`pleaselogin(?:tocontinue|withapermittedorganization:claudeauthlogin)?`,
	`pleasesignin(?:tocontinue)?`,
	`waitingforauth(?:entication(?:callback)?|orization(?:tocomplete)?)?(?:\.|…)*(?:\((?:esctocancel|pressescorctrl\+ctocancel|\^ctocancel)\))?`,
	`pleasesetanauthmethodinyour.{1,80}?orspecifyoneofthefollowingenvironmentvariablesbeforerunning:gemini_api_key,google_genai_use_vertexai,google_genai_use_gca`,
)

// loginHowTo is what such a line may go on with: what to do about it, after
// a mark ("Authentication required · Sign in again to continue", "… Run
// /login", "… (press Enter to sign in)", "… - please sign in"), then
// perhaps a hint the status row carries ("? for shortcuts"), then the row's
// end. Only those words: a command and its own ("run /login", "run `codex
// login`", "use /auth", Cursor's "Run `cursor-agent login` first."),
// signing in or logging in again, re-authenticating.
// A line that goes on in words of its own ("Authentication required for MCP
// Server: github", "… Please run /login so I paused the deploy.") is another
// login, or an agent's sentence, not the agent's status (review).
const loginHowTo = `(?:[·.:\-—(][^a-z0-9]*(?:please)?(?:` +
	`(?:run|use)['"` + "`" + `]?` + loginCommand + `['"` + "`" + `]?(?:to(?:signin(?:again)?|authenticate|continue)|first)?` +
	`|(?:signin|login)(?:again)?(?:tocontinue)?|re-?auth(?:enticate)?|pressentertosignin` +
	`)[.)]*(?:\?forshortcuts)?)?`

// loginCommand is a CLI's command to sign in, as a row reads with its spaces
// gone: named, never "any word", which would run on into a sentence
// ("/loginsoipausedthedeploy").
const loginCommand = `(?:/login|/auth|(?:claude(?:auth)?|codex|amp|cursor-agent|agent|gemini|qwen|opencode(?:auth)?|goose|crush|pi)login)`

// harnessLineRows is where those notices count: the last few rows with
// anything on them — a CLI's own status line and what sits right above its
// prompt. Higher up is the conversation, where an agent's tools print what
// other programs said: a `netlify status` answering "Not logged in. Please
// log in to see site status." is the site's login, not the agent's, and it
// once held the agent's mail and told its supervisor it could not work. The
// sign-in screens and Claude Code's own notice are specific enough to be read
// anywhere in the tail.
const harnessLineRows = 3

// harnessRows is how much of the bottom of the screen is read: the status
// line and the notice above the prompt, not the conversation. Counted in
// rows that have something on them — a sign-in dialog is mostly its own
// blank rows, and counting those left its heading unread.
const harnessRows = 12

// harnessRowsFor is how many rows are read for prog: Gemini CLI's input
// area, its status rows and the banners it keeps above them are taller
// than that, and its error for a turn sits above them all.
func harnessRowsFor(prog string) int {
	if prog == "gemini" {
		return 20
	}
	return harnessRows
}

const (
	harnessDownAfter = 2  // reads the notice must be seen in to count
	harnessUpAfter   = 10 // reads it must be gone from to clear…
	// …or this long, whichever comes first: the screen is read when it may
	// have changed, so an agent that comes up from its sign-in and then sits
	// still at its prompt gives a few reads, not ten — and was left "logged
	// out", its mail held, for good.
	harnessUpFor = 3 * time.Second
)

// harnessNow is the clock the health state is kept by (tests set it).
var harnessNow = time.Now

// harnessHealth is the debounced state; guarded by Agent.metaMu.
type harnessHealth struct {
	down       bool
	seen, gone int
	since      time.Time
	// When the notice was first seen in this run of reads, and when it was
	// first gone: what the next read is timed by (harnessReadAt).
	seenAt, goneAt time.Time
	// refused is the CLI whose words, while down, say it refused the key it
	// was given (harnessKeyRefusedIn), "" when down for want of a login.
	refused string
}

// harnessHealthTail is harnessTail with the blank rows between the rows it
// keeps: where a paragraph ends, so a row is not read as wrapping onto the
// next paragraph's first.
func harnessHealthTail(render string, n int) string {
	rows := strings.Split(render, "\n")
	at, kept := len(rows), 0
	for at > 0 && kept < n {
		if at--; strings.TrimSpace(rows[at]) != "" {
			kept++
		}
	}
	for at < len(rows) && strings.TrimSpace(rows[at]) == "" {
		at++
	}
	return strings.Join(rows[at:], "\n")
}

// harnessTail is the last n rows of a screen that have anything on them.
func harnessTail(render string, n int) string {
	var rows []string
	for _, row := range strings.Split(render, "\n") {
		if strings.TrimSpace(row) != "" {
			rows = append(rows, row)
		}
	}
	if len(rows) > n {
		rows = rows[len(rows)-n:]
	}
	return strings.Join(rows, "\n")
}

// harnessProblemIn reports whether the bottom of a screen says the agent is
// logged out. prog is the program running here, as the attention probe found
// it ("" when it is not known): only its own sign-in screen, and Claude
// Code's notice only from Claude Code, are its own.
func harnessProblemIn(tail, prog string) bool {
	if !isAgentProgram(prog) {
		// Not one the probe names as an agent ("node", "python3": an agent
		// run from a path it does not know, or a shell): not known, so every
		// CLI's sign-in screen and notice count, as before.
		prog = ""
	}
	l := harnessFlatRows(tail)
	for i, flat := range l.rows {
		if flat == "" {
			continue
		}
		bottom := i >= l.bottomFrom
		if harnessLoginScreenIn(l, i, prog) || ((prog == "" || prog == "claude") && harnessNoticeRow(flat, bottom)) {
			return true
		}
		if harnessLoginRowIn(l, i, harnessLoginLines) {
			return true // the CLI's own status, not the conversation
		}
	}
	return false
}

// harnessNoticeRow reports whether a row is Claude Code saying it is logged
// out. It says so in two places: on a line of its own in the conversation
// ("● Login expired · Please run /login"), and at the right-hand end of its
// status line ("⏵⏵ … Not logged in · Run /login"). An agent quoting the notice
// — a lead telling its owner what it saw on a report's screen — puts it in the
// middle of a sentence, and that is not this agent being logged out: reading
// it as one once marked a working lead logged out and held its mail.
func harnessNoticeRow(flat string, bottom bool) bool {
	// A row a quote opens is an agent's own text: "Login expired · Please
	// run /login", in quotes on a row of its own, is a quote of it.
	if lead := strings.IndexFunc(flat, func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }); lead > 0 && strings.ContainsAny(flat[:lead], limitQuoteGlyphs) {
		return false
	}
	core := strings.TrimFunc(flat, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	if harnessLoginRefused.MatchString(core) {
		return true // a line of its own, from its start
	}
	loc := harnessLoginNotice.FindStringIndex(core)
	if loc == nil {
		return false
	}
	if loc[0] == 0 && loc[1] == len(core) {
		return true // a line of its own
	}
	return bottom && loc[1] == len(core) // the end of the status line
}

// harnessAtWork reports whether the harness's own hook shows a turn under way
// for longer than a logged-out one lasts: one turned away ends at once ("Login
// expired" and done), so a turn still going after harnessTurnProof, with its
// screen still moving (a spinner, output), is an agent that can work —
// whatever the screen's text says of logins. What that could hide: a CLI that
// keeps its hook "working" and its screen moving after its login failed —
// for at most session.HookStateTTL (15 min), past which the hook is not
// believed. Claude Code ends a turn it turned away at once.
func harnessAtWork(hs *session.HookState, now time.Time, settledMs int64) bool {
	if hs == nil || hs.State != "working" {
		return false
	}
	age := now.Sub(hs.TS)
	return age >= harnessTurnProof && age < session.HookStateTTL && settledMs < attnSettleMs
}

// harnessTurnProof is how long a turn must have been going to show a login.
const harnessTurnProof = 10 * time.Second

// noteHarnessHealth is called with each attention tick's screen, the program
// running here ("" when not known), and whether its hook shows it at work.
func (a *Agent) noteHarnessHealth(render, prog string, atWork bool) {
	// The render carries its styling — "\x1b[1mLogin\x1b[m \x1b[1mexpired" —
	// which would sit between the words; the text is what is read.
	tail := harnessHealthTail(stripANSI(render), harnessRowsFor(prog))
	a.metaMu.Lock()
	prev := a.attnPrevFG
	a.metaMu.Unlock()
	refused := ""
	if !atWork {
		// A refused key is the CLI's own words only when that CLI is here,
		// or exited just now and left them above the shell's prompt.
		// Asked of those two by name: two CLIs' rows can read alike
		// (OpenCode and Codex both pass on OpenAI's "Incorrect API key
		// provided"), and the words are the one in front's.
		for _, p := range []string{prog, prev} {
			if p != "" && harnessKeyRefusedBy(tail, p) {
				refused = p
				break
			}
		}
	}
	bad := !atWork && (refused != "" || harnessProblemIn(tail, prog))
	now := harnessNow()
	a.metaMu.Lock()
	h := &a.harness
	changed := false
	if atWork && h.down {
		// Seen at work: logged in, now.
		h.down, h.seen, h.gone, changed = false, 0, 0, true
	} else if bad {
		if h.seen == 0 {
			h.seenAt = now
		}
		h.gone = 0
		// A CLI that refused its key and exited (no agent in front now):
		// those are its last words, not a glimpse, and count at once — a
		// read later, the agent was said to have "ended" in between.
		exited := refused != "" && !isAgentProgram(prog)
		if h.seen++; !h.down && (h.seen >= harnessDownAfter || exited) {
			h.down, h.since, changed = true, now, true
		}
	} else {
		if h.gone == 0 {
			h.goneAt = now
		}
		h.seen = 0
		if h.gone++; h.down && (h.gone >= harnessUpAfter || now.Sub(h.goneAt) >= harnessUpFor) {
			h.down, changed = false, true
		}
	}
	if !h.down {
		h.refused = ""
	} else if bad {
		h.refused = refused
	}
	down := h.down
	a.metaMu.Unlock()
	if changed && !down {
		// Logged in again: the next tick's screen reading decides the state.
		a.setAttnState("")
	}
}

// harnessReadAt is when the screen should be read again for the health
// state to settle, whatever else the screen does: a notice seen once is
// confirmed a read later, and one gone from a logged-out screen clears at
// harnessUpFor. Zero when nothing waits on a read.
func (a *Agent) harnessReadAt() time.Time {
	a.metaMu.Lock()
	defer a.metaMu.Unlock()
	h := &a.harness
	switch {
	case !h.down && h.seen > 0:
		return h.seenAt.Add(attnInterval)
	case h.down && h.gone > 0:
		return h.goneAt.Add(harnessUpFor)
	}
	return time.Time{}
}

// harnessSlowToTurnAway are the agents a turn of which runs for a while
// before it says it cannot run: pi with no model to run on works for over
// 2 s before "Error: No API key found for the selected model." Two seconds of
// work is no proof it took the message: the whole window is waited out.
var harnessSlowToTurnAway = map[string]bool{"pi": true}

// harnessKeyRefused is the CLI that, as the agent here is logged out,
// refused the key it was given ("" when it has no login at all).
func (a *Agent) harnessKeyRefused() string {
	a.metaMu.Lock()
	defer a.metaMu.Unlock()
	return a.harness.refused
}

// harnessDown reports whether the agent here is logged out.
func (a *Agent) harnessDown() bool {
	a.metaMu.Lock()
	defer a.metaMu.Unlock()
	return a.harness.down
}

// harnessTurnedAway watches, just after a message was typed in, whether the
// agent turned it away for want of a login — true as soon as it says so;
// false once it is plainly working on it, or after a while.
func (a *Agent) harnessTurnedAway() bool {
	start := time.Now()
	for time.Since(start) < 6*time.Second {
		if a.harnessDown() {
			return true
		}
		a.metaMu.Lock()
		working, prog := a.attnState == "working", strings.TrimPrefix(a.attnFG, "-")
		a.metaMu.Unlock()
		if working && time.Since(start) > 2*time.Second && !harnessSlowToTurnAway[prog] {
			return false
		}
		time.Sleep(attnInterval)
	}
	return a.harnessDown()
}
