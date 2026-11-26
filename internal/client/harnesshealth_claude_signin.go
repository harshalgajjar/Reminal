// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 Harshal Gajjar

package client

// Claude Code's sign-in notices of several sentences, each read as a whole
// paragraph (loginParagraphs): its words to the last, and nothing after.

// claudeNothingSent are Claude Code 2.1.332's notices for a message it did
// not send because the sign-in the session began on is gone, was replaced,
// or was never known. The ones of that family that are no sign-in to make
// again are not here: one that passes ("couldn't be confirmed. Try
// again."), and a key, a provider or an address set in its settings.
var claudeNothingSent = loginParagraphs("●⎿", "nothingwassent:",
	"nothingwassent:thissession.?ssign-in(?:isgone,possiblyaftera/loginor/logoutinanotherterminal|wasreplacedbyoneforadifferentorunknownorganization)\\.tocontinuehere,signinagaintotheorganizationthissessionbeganon\\.run/logininaninteractivesession,chooseanthropicconsoleaccount,thensigninwithyourconsoleaccount\\.ifanothertoolmadethissign-in,usethattool\\.touse(?:another|thenew)sign-in,startanewsession",
	"nothingwassent:theorganizationofthissession.?ssign-inwasn.?tknownwhenitbegan,sothere.?snowaytoconfirmthatthesign-inisunchanged\\.ifthissessionisinteractive,run/loginhere,chooseanthropicconsoleaccount,thensigninwithyourconsoleaccount,andsignintotheorganizationitbeganon\\.otherwisesigninagainwiththetoolthatmadethissign-in,thenstartanewsession",
)

// claudeGatewaySignIn are Claude Code's notices (2.1.330 on) for a sign-in
// to an organization's gateway, or an organization's own sign-in, that the
// person has to make again: not signed in, ended, its renewal refused, not
// accepted; a request it did not send for want of it; the organization's
// sign-in not named by this computer's settings; and, at the sign-in
// itself, a token past its time by this computer's clock, and the
// organization changed meanwhile. Its notices beside these that ask for
// another try, a restart or an administrator are not sign-ins to make.
var claudeGatewaySignIn = loginAll(
	loginParagraphs("●⎿", "youarenotsignedintoyourorganization",
		"youarenotsignedintoyourorganization.?sgateway\\.run/logininclaudecode,orrunclaudeauthlogin--organization-gatewayinaterminal",
	),
	loginParagraphs("●⎿", "yoursign-intoyourorganization",
		"yoursign-intoyourorganization.?sgatewayhasended\\.run/logininclaudecode,orrunclaudeauthlogin--organization-gatewayinaterminal",
	),
	loginParagraphs("●⎿", "therequesttorenewyoursign-in",
		"therequesttorenewyoursign-intoyourorganization.?sgatewaywasrefused\\.run/logininclaudecode,orrunclaudeauthlogin--organization-gatewayinaterminal\\.ifthesign-infailstoo,askyouradministrator",
	),
	loginParagraphs("●⎿", "yourorganization",
		"yourorganization.?sgatewaydidnotacceptyoursign-in\\.claudecodecannotrenewit\\.run/logininclaudecode,orrunclaudeauthlogin--organization-gatewayinaterminal",
	),
	loginParagraphs("●⎿", "claudecodedidnotsendthisrequest",
		"claudecodedidnotsendthisrequest\\.yourorganizationsendsclaudecode.?srequeststhroughitsgateway,whichyouaren.?tsignedinto,and/logincan.?tofferthatsign-inhere",
		"claudecodedidnotsendthisrequest\\.yoursign-intoyourorganization.?sgatewayhasexpired(?:·run/logintosignintoitagain|\\.signinginagainneedsaninteractivesession:start`?claude`?inaterminal,run/loginandsignintothegateway\\.thenrunthisagain)",
		"claudecodedidnotsendthisrequest\\.thissessionsendsitsrequeststotheapiproviderthatcamewithanorganizationsign-in,butthesessionnolongerrunsunderthatsign-in\\.usually,thisisbecauseyousignedoutorsignedintoanotheraccount,inthiswindoworinanotherwindow\\.tocontinue,run/loginandchoosetheorganizationthatyouradministratorsmanageinanthropic.?senterpriseadminconsole\\.ifclaudecodestilldoesnotsendrequests,exitandrun`?claude`?\\.anewsessionusestheaccountthatyouaresignedintonow",
	),
	loginParagraphs("●⎿", "youhaveanorganizationsign-in",
		"youhaveanorganizationsign-in\\(notaclaude\\.ailogin\\)\\.thiscomputer.?smanagedsettingsdidnotnametheorganizationofthatsign-inwhenclaudecodestarted\\.soclaudecodeusesnoneofthese:thatorganization.?scloudprovider,youranthropicapikey,oryourclaude\\.ailogin\\.touseaclaude\\.ailogin,run/logininaninteractivesession,or`?claudeauthlogin`?atyourshellprompt\\.choosetheaccountororganizationthathasyourclaudesubscription\\.thatloginreplacestheorganizationsign-in\\.orrun`?claudeauthlogout`?,whichsignsyououtofeveryaccount\\.thenstartclaudecodeagainwithyourapikey,orsignin\\.oraskyouritadministratortoaddtheorganizationtoforceloginorguuidinthiscomputer.?smanagedsettings,ifitisn.?tthereyet\\.thenstartclaudecodeagainwithnoapikeyset,becausethatsettingrefusesone",
	),
	loginParagraphs("●⎿", "bythiscomputer",
		"bythiscomputer.?sclock,thetokenfromyourorganization.?sidentityproviderhasexpiredorisabouttoexpire\\.checkthedateandtimeonthiscomputer\\.thensigninagain",
	),
	loginParagraphs("●⎿", "theorganizationsign-inchanged",
		"theorganizationsign-inchangedwhileyouweresigningintothegateway\\.run/logininclaudecodeandchoosetheorganizationthatyouradministratorsmanageinanthropic.?senterpriseadminconsole\\.thensignintoitsgateway",
	),
)
