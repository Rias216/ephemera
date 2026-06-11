# Applying this build

This archive contains the complete source tree. Extract it and run:

```bat
run.bat
```

To patch the immediately previous native-renderer build instead, copy
`upgrade-from-native-renderer.patch` to that repository and apply it from the
repository root:

```bash
patch -p1 < upgrade-from-native-renderer.patch
```

Useful commands after launch:

```text
/agent auto
/thinking on
```

Restore confirmations with `/agent safe` and hide the visible decision trace
with `/thinking off`.
