CREATE OR REPLACE VIEW open_accounts AS SELECT * FROM accounts WHERE state = 'active';
