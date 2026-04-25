# TODOS

## Evaluate SQLite -> Dolt hard cutover after stabilization

**What:** Evaluate and sequence moving more durable product state from SQLite to
Dolt once Trace, VText, and early super verification are stable.

**Why:** The current SQLite/Dolt split creates awkward boundaries. It makes trace
contracts, cross-store reasoning, and long-term product-state ownership more
annoying than they should be.

**Pros:** Could reduce split-brain semantics, simplify durable history, and make
canonical product state easier to reason about.

**Cons:** Easy to overreach. A full migration right now would distract from the
actual product bottlenecks, and hot runtime/auth coordination state may still
belong somewhere other than Dolt.

**Context:** Today runtime coordination facts mostly live in SQLite-backed store
tables while canonical VText/evidence facts live in Dolt-backed storage. Because
the product is still pre-user, when this work happens it can be a **hard
cutover**, not a long safe migration with compatibility layers. The sequence from
the review is: stabilize behavior first, narrow the cross-store contract second,
then decide what durable state should move.

**Depends on / blocked by:** VText controller hardening, Trace hard cutover,
multi-iteration VText verification, and the first super local trust gates.
