-- Reverse of 000001_initial.up.sql. Kitchen-sink drop in dependency order.
-- Provided for completeness; the big-bang cutover plan never expects to roll
-- this back in production.

DROP TABLE IF EXISTS public.game_rounds;
DROP TABLE IF EXISTS public.multiplayer_hands;
DROP TABLE IF EXISTS public.user_watchlist_items;
DROP TABLE IF EXISTS public.user_saved_locations;
DROP TABLE IF EXISTS public.user_wallets;
DROP TABLE IF EXISTS public.identity_credentials;
DROP TABLE IF EXISTS public.post_reads;
DROP TABLE IF EXISTS public.posts;
DROP TABLE IF EXISTS public.topics;
DROP TABLE IF EXISTS public.forums;
DROP TABLE IF EXISTS public.message_reactions;
DROP TABLE IF EXISTS public.chat_messages;
DROP TABLE IF EXISTS public.channel_reads;
DROP TABLE IF EXISTS public.channel_members;
DROP TABLE IF EXISTS public.channels;
DROP TABLE IF EXISTS public.audit_log;
DROP TABLE IF EXISTS public.users;

DROP EXTENSION IF EXISTS citext;
