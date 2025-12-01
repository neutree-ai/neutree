DROP TRIGGER IF EXISTS user_profile_self_update_validation ON api.user_profiles;
DROP FUNCTION IF EXISTS api.validate_user_profile_self_update();

DROP TRIGGER IF EXISTS user_profile_soft_delete_validation ON api.user_profiles;
DROP FUNCTION IF EXISTS api.validate_user_profile_soft_delete();

DROP POLICY IF EXISTS "user_profile delete policy" ON api.user_profiles;
DROP POLICY IF EXISTS "user_profile update policy" ON api.user_profiles;
DROP POLICY IF EXISTS "user_profile read policy" ON api.user_profiles;

CREATE POLICY "Profiles are viewable by everyone" ON api.user_profiles
  FOR SELECT USING (true);

CREATE POLICY "Users can update their own profile" ON api.user_profiles
  FOR UPDATE USING (id = (SELECT auth.uid()));
