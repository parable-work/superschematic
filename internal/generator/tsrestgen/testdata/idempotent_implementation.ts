/*
A typed implementation of fixture-idempotent-api's orders (D140), staged in
the generated package before it is type-checked
(TestGeneratedIdempotentRoutesEndToEnd). It keeps the orders in memory and
counts its runs, so idempotent_runtime.test.ts can check a replayed request
never reached it.
*/
import { HttpProblem, notFound } from '@superschematic/http-runtime';
import type { Order } from '@schemas/fixture-idempotent-api-types/types';
import type { CancelOrderArgs, OrdersImplementation, PlaceOrderArgs } from './interfaces';

/** The orders placed, and how often the implementation ran. */
export const placed: Order[] = [];
export const counts = { runs: 0 };

export const orders: OrdersImplementation = {
  async placeOrder(args: PlaceOrderArgs): Promise<Order> {
    counts.runs += 1;
    if (args.input.sku === 'broken') throw new Error('the order broke');
    if (args.input.sku === 'gone') throw new HttpProblem(409, 'Out of stock', { code: 'conflict' });
    const order: Order = { number: placed.length + 1, sku: args.input.sku, quantity: args.input.quantity, cancelled: false };
    placed.push(order);
    return order;
  },

  async cancelOrder(args: CancelOrderArgs): Promise<Order> {
    counts.runs += 1;
    const order = placed[args.number - 1];
    if (!order) throw notFound('No such order');
    order.cancelled = true;
    return order;
  },

  async listOrders(): Promise<Order[]> {
    return placed;
  },
};
