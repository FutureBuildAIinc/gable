// SPDX-License-Identifier: LicenseRef-OpenLBM-Surface-1.0
// SPDX-FileCopyrightText: 2026 FutureBuild, Inc. and OpenLBM contributors

//! The profile manifest of the Gable shells: one Tauri 2 workspace, three
//! thin profiles that differ in identifier, the staff roles that open them,
//! the app the window opens first, the device grants and the window
//! defaults.
//!
//! The roles are the core's own role words (the seed's role column, the role
//! middleware's sets): the office and counter roles open the work shell, the
//! yard and driving roles open the field shell, and the pro shell is the
//! contractors' frame, opened from the portal side, so no staff role opens
//! it. The device grants are Field only: camera and fine location for proof
//! of delivery photos and drive runs; Work and Pro hold none, and each
//! profile's own capability file, never a shared grant, is what enforces
//! that. The cashier opens both the work shell and the field shell: the
//! yard sale is the counter made touch first, so the counter's money taker
//! also opens the handheld, and the launcher shows both.
//!
//! The start app names the shell app the profile opens first. Until a
//! shell's own bundle lands, the profile's window serves the front door
//! bundle (the one shell bundle that exists), and the name is what the
//! launcher will open; the tests pin the names and the identifiers against
//! each profile's own `tauri.conf.json`.

use std::sync::OnceLock;

/// Which release channel a profile updates from. The channel is a profile
/// fact, never a webview choice; the update endpoints land with the shells
/// that use them.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Channel {
    Work,
    Field,
    Pro,
}

impl Channel {
    pub fn as_str(self) -> &'static str {
        match self {
            Channel::Work => "work",
            Channel::Field => "field",
            Channel::Pro => "pro",
        }
    }
}

/// The device permissions a profile grants. Camera and fine location are
/// Field only; the enforcement is each profile's own Tauri capability file,
/// never a shared open grant.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct Permissions {
    pub camera: bool,
    pub fine_location: bool,
}

/// The main window's defaults on a desktop. A phone or tablet sizes the
/// window itself; these are the dev and fallback values, phone portrait for
/// the field shell.
#[derive(Clone, Copy, Debug, PartialEq)]
pub struct WindowDefaults {
    pub title: &'static str,
    pub width: f64,
    pub height: f64,
}

/// A complete profile.
#[derive(Debug)]
pub struct Profile {
    pub identifier: &'static str,
    pub product_name: &'static str,
    pub channel: Channel,
    /// The staff roles that open this profile on the launcher.
    pub roles: &'static [&'static str],
    /// The app the window opens first (the shell's bundle route).
    pub start_app: &'static str,
    pub window: WindowDefaults,
    pub permissions: Permissions,
}

/// Every profile, in channel order.
pub fn all() -> [&'static Profile; 3] {
    [work(), field(), pro()]
}

/// The profile a staff role opens on the launcher.
pub fn profile_for_role(role: &str) -> Option<&'static Profile> {
    all().into_iter().find(|p| p.roles.contains(&role))
}

/// The Work profile: the office and counter roles, desktop and tablet, no
/// device grant.
pub fn work() -> &'static Profile {
    static WORK: OnceLock<Profile> = OnceLock::new();
    WORK.get_or_init(|| Profile {
        identifier: "ai.futurebuild.gable.work",
        product_name: "Gable Work",
        channel: Channel::Work,
        roles: &[
            "owner",
            "admin",
            "finance",
            "sales",
            "cashier",
            "purchasing",
        ],
        start_app: "work-shell",
        window: WindowDefaults {
            title: "Gable Work",
            width: 1280.0,
            height: 800.0,
        },
        permissions: Permissions {
            camera: false,
            fine_location: false,
        },
    })
}

/// The Field profile: the yard and driving roles, phone first, and the only
/// profile with a camera or fine location. The cashier opens it too: the
/// yard sale is the counter made touch first, so the one role that may take
/// money opens the handheld as well as the work shell; no money class moves.
pub fn field() -> &'static Profile {
    static FIELD: OnceLock<Profile> = OnceLock::new();
    FIELD.get_or_init(|| Profile {
        identifier: "ai.futurebuild.gable.field",
        product_name: "Gable Field",
        channel: Channel::Field,
        roles: &["warehouse", "driver", "cashier"],
        start_app: "field-shell",
        window: WindowDefaults {
            title: "Gable Field",
            width: 412.0,
            height: 915.0,
        },
        permissions: Permissions {
            camera: true,
            fine_location: true,
        },
    })
}

/// The Pro profile: the contractors' frame, opened from the portal side, so
/// it holds no staff role.
pub fn pro() -> &'static Profile {
    static PRO: OnceLock<Profile> = OnceLock::new();
    PRO.get_or_init(|| Profile {
        identifier: "ai.futurebuild.gable.pro",
        product_name: "Gable Pro",
        channel: Channel::Pro,
        roles: &[],
        start_app: "pro-shell",
        window: WindowDefaults {
            title: "Gable Pro",
            width: 1280.0,
            height: 800.0,
        },
        permissions: Permissions {
            camera: false,
            fine_location: false,
        },
    })
}

#[cfg(test)]
mod tests {
    use super::*;

    /// The role words the core itself knows (the role middleware's sets and
    /// the seed's role column). A profile role that is not one of these is a
    /// manifest typo the launcher could never match.
    const KNOWN_ROLES: [&str; 8] = [
        "owner",
        "admin",
        "finance",
        "sales",
        "cashier",
        "purchasing",
        "warehouse",
        "driver",
    ];

    #[test]
    fn identifiers_and_products_and_channels_are_distinct() {
        let ids: Vec<&str> = all().iter().map(|p| p.identifier).collect();
        let names: Vec<&str> = all().iter().map(|p| p.product_name).collect();
        assert_eq!(
            ids.iter().collect::<std::collections::BTreeSet<_>>().len(),
            3
        );
        assert_eq!(
            names
                .iter()
                .collect::<std::collections::BTreeSet<_>>()
                .len(),
            3
        );
        assert_ne!(work().channel, field().channel);
        assert_ne!(field().channel, pro().channel);
    }

    #[test]
    fn every_identifier_matches_its_own_tauri_config() {
        // The profile model and tauri.conf.json must name the same
        // identifier and product: the config is what the OS installs, the
        // model is what the Rust side reasons with. include_str keeps the
        // pin working on a fresh clone.
        for (profile, config) in [
            (work(), include_str!("../../gable-work/tauri.conf.json")),
            (field(), include_str!("../../gable-field/tauri.conf.json")),
            (pro(), include_str!("../../gable-pro/tauri.conf.json")),
        ] {
            let value: serde_json::Value =
                serde_json::from_str(config).expect("tauri.conf.json parses");
            assert_eq!(
                value["identifier"].as_str(),
                Some(profile.identifier),
                "{} drifted",
                profile.identifier
            );
            assert_eq!(
                value["productName"].as_str(),
                Some(profile.product_name),
                "{} drifted",
                profile.identifier
            );
        }
    }

    #[test]
    fn the_office_and_counter_roles_open_work() {
        for role in [
            "owner",
            "admin",
            "finance",
            "sales",
            "cashier",
            "purchasing",
        ] {
            assert_eq!(
                profile_for_role(role).map(|p| p.identifier),
                Some(work().identifier),
                "{role}"
            );
        }
    }

    #[test]
    fn the_yard_and_driving_roles_open_field() {
        for role in ["warehouse", "driver"] {
            assert_eq!(
                profile_for_role(role).map(|p| p.identifier),
                Some(field().identifier),
                "{role}"
            );
        }
    }

    #[test]
    fn no_staff_role_opens_pro_and_an_unknown_role_opens_nothing() {
        assert!(pro().roles.is_empty());
        for role in KNOWN_ROLES {
            assert_ne!(
                profile_for_role(role).map(|p| p.identifier),
                Some(pro().identifier),
                "{role} must never open pro"
            );
        }
        assert!(profile_for_role("stranger").is_none());
        assert!(profile_for_role("").is_none());
    }

    #[test]
    fn every_role_name_is_a_role_the_core_knows() {
        for p in all() {
            for role in p.roles {
                assert!(
                    KNOWN_ROLES.contains(role),
                    "{} names unknown role {role}",
                    p.identifier
                );
            }
        }
        // And the set is fully claimed: every known role opens something.
        for role in KNOWN_ROLES {
            assert!(profile_for_role(role).is_some(), "{role} opens nothing");
        }
    }

    #[test]
    fn every_role_opens_exactly_one_profile_except_the_cashier() {
        // The yard sale ruling: the cashier holds the counter's money class
        // and also opens the handheld, so cashier is the one deliberate
        // overlap; any other role claimed twice is a mistake.
        for a in all() {
            for role in a.roles {
                if *role == "cashier" {
                    assert!(field().roles.contains(role));
                    continue;
                }
                assert_eq!(
                    profile_for_role(role).map(|p| p.identifier),
                    Some(a.identifier),
                    "{role} is claimed twice"
                );
            }
        }
        assert!(work().roles.contains(&"cashier"));
        assert!(field().roles.contains(&"cashier"));
    }

    #[test]
    fn the_start_apps_name_the_three_shells() {
        assert_eq!(work().start_app, "work-shell");
        assert_eq!(field().start_app, "field-shell");
        assert_eq!(pro().start_app, "pro-shell");
    }

    #[test]
    fn device_permissions_are_field_only() {
        assert!(field().permissions.camera && field().permissions.fine_location);
        assert!(!work().permissions.camera && !work().permissions.fine_location);
        assert!(!pro().permissions.camera && !pro().permissions.fine_location);
    }

    #[test]
    fn the_field_window_is_phone_portrait_and_the_others_are_landscape() {
        assert!(field().window.height > field().window.width);
        assert!(work().window.width > work().window.height);
        assert!(pro().window.width > pro().window.height);
        assert!(work().window.width >= 1024.0);
    }
}
