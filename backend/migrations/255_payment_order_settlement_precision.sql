-- Preserve converted EasyPay overpayments down to the precision of user balances.
-- Keep the existing integer range for receipts and full-order refunds.
ALTER TABLE payment_orders
    ALTER COLUMN amount TYPE DECIMAL(26,8),
    ALTER COLUMN pay_amount TYPE DECIMAL(26,8),
    ALTER COLUMN refund_amount TYPE DECIMAL(26,8);
