# Smart Garage — Detailed UI Layout Specs

## Brand
- Primary green: `#14532D` / container `#DCFCE7`
- Surface: light `#F3F5F7`, dark `#0F172A`
- Cards: 16px radius, 12–16px padding, soft elevation
- Primary CTA: full-width FilledButton, height ≥ 48, weight 700
- Secondary: OutlinedButton same height

## Car owner — Booking sequence
1. Nearby garages — map top 45%, list cards bottom with Book CTA
2. Garage profile — services with price bold right, sticky Continue
3. Vehicle list — body type badge (SUV/Pickup set at registration)
4. Date/time — day chips + time grid
5. Confirmation — service, car, price, time summary card + Confirm booking
6. Track — map 220–280px, green pickup + blue provider + polyline; after arrived hide map/cancel
7. Satisfaction → Bill → Pay → wait verify → Rate (one step visible at a time)

## Car owner — Mechanic request
Vehicle → location → service/custom → confirmation → searching → live track → same post-service chain

## Provider — Incoming card
Accent bar, customer/vehicle/problem, Accept primary, Deny with reason sheet

## Provider — Active job phases
Accepted (garage wait arrival) → En route map → Arrived Start → Timer Finish → Wait satisfaction → Send bill → Confirm paid → Rate

## Admin
Dashboard KPIs + recent requests; Users create all roles; Reviews tabs by actor; Roles search by name

## Settings (both apps)
Section labels + SettingsCard: Preferences, Account, Support, Sign out
