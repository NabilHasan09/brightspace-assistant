// Package brightspace mirrors the D2L Valence REST API.
//
// The Client interface maps 1:1 onto documented Valence routes so that live
// responses unmarshal into these types with no translation layer. It has two
// implementations: MockClient (fixtures, works today) and LiveClient (OAuth2,
// blocked on institutional credentials). Everything above this package is
// identical either way.
//
// Build step 1.
package brightspace
