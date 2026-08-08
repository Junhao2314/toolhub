# Allow Explicit Plaintext Secret Bundles

Standard Profile Bundles omit Secret values, while a separately named export may include only the revision's referenced values in plaintext after current-password reauthentication. This deliberate credential-backup exception avoids password-derived bundle cryptography and cross-installation key coupling; the response is never stored, uses `no-store`, and the UI must make the irreversible exposure explicit.
