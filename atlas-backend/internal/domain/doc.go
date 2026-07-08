// Package domain contains persistence-free Atlas business rules.
//
// Domain helpers may inspect and transform models, but they must not query the
// database, open transactions, publish network messages, or depend on postgres.
// Services and repositories can therefore reuse the same rules inside one
// TxManager-owned workflow without hiding transaction behavior.
package domain
