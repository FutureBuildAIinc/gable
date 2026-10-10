// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

// The delivery module's wire shapes (ADR 0001, item C5-1d): lowercase
// vocabularies, business dates as YYYY-MM-DD, optional fields present as
// null, and a revision on every record.

export type VehicleType = 'box_truck' | 'flatbed' | 'pickup' | 'van' | 'crane';
export type DriverStatus = 'active' | 'inactive' | 'on_leave';
export type RouteStatus = 'draft' | 'scheduled' | 'in_transit' | 'completed' | 'cancelled';
export type DeliveryStatus = 'pending' | 'out_for_delivery' | 'delivered' | 'failed' | 'partial';
export type AdjustReasonCode = 'short_ship' | 'damaged' | 'refused' | 'wrong_product' | 'other';

export interface Vehicle {
    id: string;
    name: string;
    vehicle_type: VehicleType;
    license_plate: string;
    capacity_weight_lbs: number | null;
    vin: string | null;
    year: number | null;
    make: string | null;
    model: string | null;
    /** Business date, YYYY-MM-DD. */
    insurance_expiry: string | null;
    /** Business date, YYYY-MM-DD. */
    next_service_date: string | null;
    odometer_miles: number | null;
    notes: string | null;
    photo_url: string | null;
    revision: number;
    created_at: string;
    updated_at: string;
}

export interface Driver {
    id: string;
    name: string;
    license_number: string | null;
    status: DriverStatus;
    phone_number: string | null;
    cdl_class: string | null;
    /** Business date, YYYY-MM-DD. */
    cdl_expiry: string | null;
    /** Business date, YYYY-MM-DD. */
    hire_date: string | null;
    email: string | null;
    photo_url: string | null;
    revision: number;
    created_at: string;
    updated_at: string;
}

export interface Delivery {
    id: string;
    route_id: string | null;
    order_id: string;
    order_number: string | null;
    stop_sequence: number;
    status: DeliveryStatus;

    // POD
    pod_proof_url: string | null;
    pod_signed_by: string | null;
    pod_timestamp: string | null;
    signature_data_url: string | null;

    delivery_instructions: string | null;

    created_at: string;
    updated_at: string;

    // Joined
    customer_name: string | null;
    address: string | null;
    latitude: number | null;
    longitude: number | null;

    // ETA (from route optimization)
    estimated_arrival: string | null;
    scheduled_start: string | null;
    scheduled_end: string | null;
    revision: number;
}

export interface Route {
    id: string;
    vehicle_id: string;
    driver_id: string;
    /** Business date, YYYY-MM-DD. */
    scheduled_date: string;
    status: RouteStatus;
    notes: string | null;
    total_duration_mins: number | null;
    total_distance_miles: number | null;
    revision: number;
    created_at: string;
    updated_at: string;

    // Joined
    vehicle_name: string;
    driver_name: string;
    stop_count: number;
    /** The route's stops, present on the board read (include=stops) and the route document; null on a plain list page. */
    stops: Delivery[] | null;
}

export interface CreateVehicleRequest {
    name: string;
    vehicle_type: VehicleType;
    license_plate: string;
    capacity_weight_lbs?: number;
    vin?: string;
    year?: number;
    make?: string;
    model?: string;
    insurance_expiry?: string;
    next_service_date?: string;
    odometer_miles?: number;
    notes?: string;
}

export type UpdateVehicleRequest = CreateVehicleRequest;

export interface CreateDriverRequest {
    name: string;
    license_number?: string;
    phone_number?: string;
    cdl_class?: string;
    cdl_expiry?: string;
    hire_date?: string;
    email?: string;
}

export interface UpdateDriverRequest {
    name: string;
    license_number?: string;
    phone_number?: string;
    status: DriverStatus;
    cdl_class?: string;
    cdl_expiry?: string;
    hire_date?: string;
    email?: string;
}

export interface CreateRouteRequest {
    vehicle_id: string;
    driver_id: string;
    scheduled_date: string;
    notes?: string;
}

export interface AssignOrderRequest {
    route_id: string;
    order_id: string;
    stop_sequence?: number;
    delivery_instructions?: string;
}

export interface TransitionDeliveryRequest {
    to: Extract<DeliveryStatus, 'delivered' | 'failed' | 'partial'>;
    pod_proof_url?: string;
    pod_signed_by?: string;
    signature_data_url?: string;
}

export interface CapacityWarning {
    vehicle_capacity_lbs: number;
    current_load_lbs: number;
    order_weight_lbs: number;
    total_after_lbs: number;
}

export interface AssignOrderResult {
    delivery: Delivery;
    capacity_warning: CapacityWarning | null;
}

/** One page of a cursor list envelope. */
export interface Page<T> {
    items: T[];
    next_cursor: string | null;
    limit: number;
    total?: number;
}
