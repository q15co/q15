# MIT, with a DCO and no CLA

Status: accepted

q15 is MIT. Apache-2.0 was the live alternative and was rejected on three grounds: its patent grant
and retaliation clause pay off at organisation scale, and this project has no patents and no
product; Apache-2.0 is incompatible with GPL-2.0, where MIT flows anywhere; and Apache section 6
only disclaims trademark rights, so "a fork has to rename" is a sentence in
[CONTRIBUTING.md](../../CONTRIBUTING.md) rather than a licence term. Contributions arrive under the
Developer Certificate of Origin and there is no copyright assignment, because inbound terms that
match outbound terms are the shortest thing to explain to a stranger.

## Consequences

- Anyone may use, vendor or relicense this work downstream, including inside a product. That is the
  intended reading, not a loophole.
- Changing the licence later needs the agreement of every contributor. At one contributor that is
  nothing at all, which is precisely why the choice is worth recording now instead of leaving it
  open.
- Dependency licences are tracked rather than discovered: [THIRD-PARTY.md](../../THIRD-PARTY.md)
  enumerates what the binaries and the browser bundle actually ship.

Evidence: [LICENSE](../../LICENSE), [CONTRIBUTING.md](../../CONTRIBUTING.md),
[THIRD-PARTY.md](../../THIRD-PARTY.md), [#252](https://github.com/q15co/q15/pull/252).
