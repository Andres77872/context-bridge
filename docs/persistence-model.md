# Persistence model

Context Bridge uses a user-local SQLite database in WAL mode. Captures belong
to the resolved root session and receive numbers from that root's durable
`next_seq` counter. Deleting a capture does not reuse its sequence number.

Late parent discovery may move captures from a provisional root into the
authoritative root in one transaction. During that migration, call IDs are
deduplicated and new root-local sequence numbers are assigned.

Persistence is bounded by all of the following:

- 256 KiB maximum persisted capture content;
- 1,000 captures per root;
- 10,000 captures and 256 MiB of logical content globally;
- 30-day capture retention.

Queries filter expired and dashboard-deleted data even before the next pruning
write. Logical row deletion does not guarantee immediate SQLite or WAL file
shrink.

Input validation covers bounded identifiers, cycle/conflicting-parent
rejection, safe literal database paths, credential/private-block redaction,
and explicit pattern rejection in regex search. Redaction remains best effort;
callers must not intentionally persist credentials.
