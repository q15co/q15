# Facts

A synthetic corpus file for the duplicate-rate measurement; none of this text is memory content. The
two claims below carry no metadata span, so the markdown formatter cannot reshape the claim text
they are measured on. They are the at-threshold pair: they differ in one word, so eight of their ten
normalized tokens are shared and the token overlap lands exactly on 0.8, the boundary at or above
which a pair counts as a duplicate (DESIGN.md §7.3).

- [c-2026-0101] The river ferry departs before sunrise on cold mornings.

- [c-2026-0102] The river ferry departs before sunrise on foggy mornings.
