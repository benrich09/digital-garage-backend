# Flutter go_router patterns (Smart Garage)

## Shell tabs (car owner)
```dart
StatefulShellRoute.indexedStack(
  builder: (_, __, shell) => AppShell(shell: shell),
  branches: [
    StatefulShellBranch(routes: [GoRoute(path: '/', ...)]),
    StatefulShellBranch(routes: [GoRoute(path: '/history', ...)]),
    StatefulShellBranch(routes: [GoRoute(path: '/profile', ...)]), // Settings
  ],
)
```
Indexed stack keeps tab state when switching.

## Auth redirect
```dart
redirect: (context, state) {
  final session = supabase.auth.currentSession;
  final loggingIn = state.matchedLocation == '/login' || ...;
  if (session == null && !loggingIn) return '/welcome';
  if (session != null && loggingIn) return '/';
  return null;
},
refreshListenable: GoRouterRefreshStream(supabase.auth.onAuthStateChange),
```

## Nested job flow (push stack)
```
/request/mechanic → /request/:id/searching → /request/:id/track → /rate/:id
/garages/nearby → /garages/:id → book → track
```
Use `context.push` for stack; `context.go` for tab roots / post-logout.

## Query params
```dart
GoRoute(
  path: '/rate/:requestId',
  builder: (_, s) => RatingScreen(
    requestId: s.pathParameters['requestId']!,
    bookingId: s.uri.queryParameters['bookingId'],
  ),
)
```

## Avoid
- Building routes from unsanitized deep links without auth check
- `go` when you need `pop` back to track screen (prefer `push`)
