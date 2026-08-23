# Push notification integration

## Current state
- Backend PushService: FCM when credentials set; no-op otherwise
- Flutter PushService: saves fcm_token on profiles; SnackBar fallback
- OS tray needs Firebase + google-services.json

## Flow
Event → Go Notify(userId) → FCM → device tray
Parallel: WebSocket status_update for in-app UI

## Key push events
New request/booking, accept, en_route, arrived, bill ready, payment claim, payment confirmed, deny, rework

## Setup
1. Firebase project + Android apps
2. google-services.json → android/app/
3. flutterfire configure
4. firebase_core, firebase_messaging, flutter_local_notifications
5. Render: FCM service account / project id
6. profiles.fcm_token columns (migration 0023)

## Without Firebase
WS + REST poll (6–8s) + in-app SnackBar still work
