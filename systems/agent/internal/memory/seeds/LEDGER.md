# Conversion Ledger

One line per memory exit (`converted`, `moved`, `dropped`, `superseded`, `resolved`, `retracted`,
`closed`), written as `YYYY-MM-DDTHH:MMZ src=t<a>..t<b> run=<job>` followed by indented detail
lines; append-only, rotated to `working/ledger/YYYY-Q.md` past 256 KB, never prompt-injected.
