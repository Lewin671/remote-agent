require 'minitest/autorun'
require 'tmpdir'
require 'fileutils'
require_relative 'publishing'

class PublishingTest < Minitest::Test
  Build = Struct.new(:version)
  Version = Struct.new(:version_string, :app_version_state, :release_type, :build)

  def setup
    @dir = Dir.mktmpdir
    @previous = ENV['IOS_SOURCE_DIR']
    ENV['IOS_SOURCE_DIR'] = @dir
    File.write(File.join(@dir, 'version.properties'), "version=0.56.2\nandroidVersionCode=163\n")
  end

  def teardown
    ENV['IOS_SOURCE_DIR'] = @previous
    FileUtils.remove_entry(@dir)
  end

  def test_first_update_and_editable_retry
    assert_equal :publish, Publishing.decision([Version.new('0.55.0', 'READY_FOR_DISTRIBUTION', 'MANUAL')])
    assert_equal :publish, Publishing.decision([Version.new('0.56.2', 'PREPARE_FOR_SUBMISSION', 'AFTER_APPROVAL')])
    assert_equal '163', Publishing.build_number
  end

  def test_submitted_or_live_automatic_version_is_idempotent
    %w[WAITING_FOR_REVIEW IN_REVIEW READY_FOR_DISTRIBUTION].each do |state|
      assert_equal :done, Publishing.decision([Version.new('0.56.2', state, 'AFTER_APPROVAL', Build.new('163'))])
    end
  end

  def test_never_withdraws_previous_review_or_overwrites_newer_version
    %w[WAITING_FOR_REVIEW IN_REVIEW PREPARE_FOR_SUBMISSION PENDING_DEVELOPER_RELEASE].each do |state|
      assert_raises(RuntimeError) { Publishing.decision([Version.new('0.55.0', state, 'MANUAL')]) }
    end
    assert_raises(RuntimeError) { Publishing.decision([Version.new('0.57.0', 'READY_FOR_DISTRIBUTION', 'AFTER_APPROVAL')]) }
  end

  def test_manual_release_is_not_mistaken_for_success
    assert_raises(RuntimeError) { Publishing.decision([Version.new('0.56.2', 'PENDING_DEVELOPER_RELEASE', 'MANUAL')]) }
  end

  def test_publication_requires_exact_clean_annotated_tag
    Publishing.git('init', '-q')
    Publishing.git('config', 'user.name', 'Release test')
    Publishing.git('config', 'user.email', 'release@example.invalid')
    Publishing.git('add', 'version.properties')
    Publishing.git('commit', '-qm', 'Release fixture')
    Publishing.git('tag', 'v0.56.2')
    assert_raises(RuntimeError) { Publishing.validate_source! }
    Publishing.git('tag', '-d', 'v0.56.2')
    Publishing.git('tag', '-a', 'v0.56.2', '-m', 'Improved chat scrolling')
    Publishing.validate_source!
    assert_equal 'Improved chat scrolling', Publishing.notes
    File.write(File.join(@dir, 'untracked'), 'dirty')
    assert_raises(RuntimeError) { Publishing.validate_source! }
  end


  def test_wrong_submitted_build_is_not_reported_as_success
    version = Version.new('0.56.2', 'WAITING_FOR_REVIEW', 'AFTER_APPROVAL', Build.new('2'))
    assert_raises(RuntimeError) { Publishing.decision([version]) }
  end


  def test_invalid_credentials_cannot_appear_in_public_error_logs
    name = 'IOS_PUBLISH_TEST_SECRET'
    ENV[name] = '{"key":"credential-sentinel"'
    error = assert_raises(RuntimeError) { Publishing.secret(name, %w[key]) }
    refute_includes error.message, 'credential-sentinel'
    assert_nil error.cause
    ENV[name] = '"credential-sentinel"'
    error = assert_raises(RuntimeError) { Publishing.secret(name, %w[key]) }
    refute_includes error.message, 'credential-sentinel'
    ENV[name] = '{"key":"credential-sentinel"}'
    assert_equal 'credential-sentinel', Publishing.secret(name, %w[key]).fetch('key')
  ensure
    ENV.delete(name)
  end


  def test_only_the_app_release_uses_the_profile_and_the_tag_is_restored_on_error
    project_path = File.join(@dir, 'ios/RemoteAgent.xcodeproj')
    FileUtils.mkdir_p(File.dirname(project_path))
    project = Xcodeproj::Project.new(project_path)
    project.new_target(:application, 'RemoteAgent', :ios, '17.0')
    project.new_target(:bundle, 'PackageResources', :ios, '17.0')
    project.save
    Publishing.git('init', '-q')
    Publishing.git('config', 'user.name', 'Release test')
    Publishing.git('config', 'user.email', 'release@example.invalid')
    Publishing.git('add', '.')
    Publishing.git('commit', '-qm', 'Signing fixture')
    Publishing.git('tag', '-a', 'v0.56.2', '-m', 'Signing fixture')
    path = File.join(project_path, 'project.pbxproj')
    original = File.binread(path)
    previous = ENV['RA_SOURCE_COMMIT']
    ENV['RA_SOURCE_COMMIT'] = 'previous-value'
    Publishing.with_app_signing('EXAMPLETEAM', 'Example App Store') do
      configured = Xcodeproj::Project.open(project_path)
      configured.targets.each do |target|
        target.build_configurations.each do |config|
          if target.name == 'RemoteAgent' && config.name == 'Release'
            assert_equal 'Example App Store', config.build_settings['PROVISIONING_PROFILE_SPECIFIER']
            assert_equal 'Apple Distribution', config.build_settings['CODE_SIGN_IDENTITY']
          else
            assert_nil config.build_settings['PROVISIONING_PROFILE_SPECIFIER']
          end
        end
      end
      assert_equal Publishing.git('rev-parse', '--short=12', 'HEAD'), ENV['RA_SOURCE_COMMIT']
    end
    assert_equal original, File.binread(path)
    assert_equal 'previous-value', ENV['RA_SOURCE_COMMIT']
    assert_raises(RuntimeError) { Publishing.with_app_signing('EXAMPLETEAM', 'Example App Store') { raise 'Build failure' } }
    assert_equal original, File.binread(path)
    assert_equal 'previous-value', ENV['RA_SOURCE_COMMIT']
    Publishing.validate_source!
  ensure
    ENV['RA_SOURCE_COMMIT'] = previous
  end

end
