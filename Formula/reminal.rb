class Reminal < Formula
  desc "Remote terminal access — secure, zero-config alternative to SSH"
  homepage "https://github.com/harshalgajjar/Reminal"
  version "1.8.2"
  license "AGPL-3.0-or-later"

  head do
    url "https://github.com/harshalgajjar/Reminal.git", branch: "main"
  end

  on_macos do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v1.8.2/reminal_1.8.2_darwin_arm64.tar.gz"
      sha256 "ef358e8f8beed66785088e370f9c3616b6ff41c329ad51aa4d61287fb9872e12"
    end
    on_intel do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v1.8.2/reminal_1.8.2_darwin_amd64.tar.gz"
      sha256 "57dbbb273e5c712000a7136db7a684a6fcc51651e48a69b71f04e902dbb54d43"
    end
  end

  on_linux do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v1.8.2/reminal_1.8.2_linux_arm64.tar.gz"
      sha256 "c9dcf1dfdccf6c1d816951e79da50929825d6a834c277c6e7bb952ae157ef841"
    end
    on_intel do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v1.8.2/reminal_1.8.2_linux_amd64.tar.gz"
      sha256 "b902e2e128e68d3bd2fca4e9b3e9e64111eb378afcb488404962d9707d7f7e68"
    end
  end

  depends_on "go" => :build if build.head?

  def install
    if build.head?
      system "go", "build", "-ldflags=#{ldflags}", "-o", bin/"reminal", "./cmd/reminal"
    else
      bin.install "reminal"
    end
  end

  def ldflags
    "-s -w " \
      "-X main.version=#{version} " \
      "-X github.com/reminal/reminal/internal/config.DefaultCloudRelay=wss://reminal-relay.futuristic.workers.dev/ws " \
      "-X github.com/reminal/reminal/internal/config.DefaultCloudWeb=https://reminal-relay.futuristic.workers.dev"
  end

  def caveats
    <<~EOS
      reminal connects to the hosted relay automatically — no setup needed.

        reminal              # share your terminal
        reminal --connect ID --pin PIN
    EOS
  end

  test do
    assert_match version.to_s, shell_output("#{bin}/reminal version")
  end
end
