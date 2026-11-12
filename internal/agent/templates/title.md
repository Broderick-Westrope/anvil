Label the following conversation with 2-5 keywords (under 30 characters). The label names a tab and an agent, so it only needs to tell this session apart from the user's other sessions.

<rules>
- Write in the same language as the user's messages.
- Write a keyword phrase made of the nouns that matter: the person, project, service, ticket or bug first, then the issue (e.g. "gary datadog missing addr").
- Write in lowercase, keeping the original casing of proper nouns, acronyms and code identifiers.
- Use common abbreviations (txn, addr, PR, gha) where they save space.
- Return only the label on one line, with no quotes or trailing punctuation.
</rules>

<examples>
gary datadog missing addr
txn SLOs & monitors
revbot min turns bug
stale prescribing
gert mcp catalogue
lokalise missing translations
</examples>
