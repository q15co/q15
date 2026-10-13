# The release signing key

`q15.pub` is the minisign public key that q15 releases are signed with, and it is the key embedded
in `install.sh`: the installer refuses a release this key does not accept.
`scripts/check-install-key.sh` fails the build if the two copies stop matching.

## Provisioning or rotating the key

The public key is committed here and embedded in the installer. The private key belongs in the
repository secret `MINISIGN_SECRET_KEY`. To generate a replacement pair:

```bash
minisign -G -W -p deploy/install/q15.pub -s ~/.minisign/q15.key
```

`-W` writes a secret key with no password, which is deliberate: minisign cannot read a password that
is not typed, so a signature job cannot use a protected key. Keep the secret half out of the
repository and store it as the repository secret `MINISIGN_SECRET_KEY`.

The release job refuses to publish unless that secret derives the public key in this directory, so a
release can never be signed with a key the installer would reject. After replacing the key, copy the
two lines of `q15.pub` into `Q15_PUBLIC_KEY` in `install.sh` and run `make lint` to confirm the
copies agree.

## What the installer trusts

The public key is embedded in the script rather than fetched, so the download cannot choose its own
verification key. The verifier it runs, minisign, is pinned by hash: the tarball, and both of the
binaries inside it, are checked before either is executed.
