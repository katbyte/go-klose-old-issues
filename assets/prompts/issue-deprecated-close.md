You are triaging OPEN GitHub issues for the terraform-provider-azurerm provider. Each numbered issue below references one or more resources, data sources, or properties that have been REMOVED from the provider or formally DEPRECATED, listed with when it happened, the source (a major-version upgrade guide or the changelog), and the successor to use instead when one exists. For property-level matches you are also shown the issue line the property matched in. When the issue also references resources that are NOT removed or deprecated, those are listed too — weigh them hard: an issue whose substance lives on one of those is not moot, whatever else it mentions. You are given the issue's title, body, dates, and recent comments.

For each issue judge how likely its substance is moot where it stands, meaning the issue is really about the removed or deprecated thing itself, so that closing it as not planned and pointing at the successor is the right call. Score HIGH when the issue is a feature request for, a bug report against, or a question about the removed resource or property specifically: asks to extend a removed resource, bugs that only occur in it, requests to change a removed property. The ask being reasonable does not matter; if the thing it targets is gone from the provider, the issue as filed cannot be acted on and a fresh issue against the successor is the correct path.

Score LOW when:

- The reference is incidental: the resource or property merely appears in a configuration snippet or error log while the actual problem or request concerns something that still exists.
- The issue equally or primarily concerns a resource that still exists in the provider; do not close a live ask because a dead resource appears alongside it.
- The evidence is a deprecation only, not a removal. Deprecated things still work; closing is premature unless the issue is specifically asking to change or extend the deprecated thing itself.
- The comments show the discussion has moved to the successor and remains unresolved there; that is a live issue wearing an old title.

Be conservative: when it is unclear whether the substance targets the dead thing or a live one, score low.

Respond with ONLY a JSON array, one entry per issue, no other text:
[{"number": <int>, "confidence": <0.0 to 1.0>, "reason": "<one sentence naming the removed thing and why the issue is or is not moot>"}]

Issues:
