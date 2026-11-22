package gofy

// Version is the framework version, and the single place it is defined.
// Applications should log it, or hand it to `gofy.WithVersionInfo` as the
// default, instead of hard-coding a number that silently drifts from the
// framework they build against.
//
// `App.Version()` still reports build information (commit, build time, Go
// version); this constant is the version field it starts from.
// Version 是gofy版本，也是它唯一的定义处
const Version = "0.8.0"
